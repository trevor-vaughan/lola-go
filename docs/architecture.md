# Architecture

For contributors. The design spec is `docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`;
this page describes what is built (Plans 1 to 4; 5a, sources, designed in
`docs/superpowers/specs/2026-10-09-plan-5a-sources-design.md`; 5b, module formats and runtime
hooks, designed in `docs/superpowers/specs/2026-10-09-plan-5b-formats-design.md`; and 5c, the
interactive install flow, designed in
`docs/superpowers/specs/2026-10-10-plan-5c-install-flow-design.md`; and `lola migrate`, designed
in `docs/superpowers/specs/2026-10-12-migrate-from-python-lola-design.md`).

## Pipeline

```
source ──fetch──▶ staging ──Put (detect, normalize, digest)──▶ cache (versions, heads per ref)
                                      │ module.Load (IR, re-derived each time; no format adapter)
                                      ▼
                     resolver (cached module minus selections.yaml exclusions)
                                      ▼
                     targets (pure: IR → intents + findings)
                                      ▼
            runtime hook consent (drops unconsented hook groups)
                                      ▼
            plan.Compute (diff vs ledger + disk, conflict matrix) ──▶ Plan
                                      ▼
            hook consent ─▶ module copies swapped in ─▶ pre-install hooks
                                      ▼
            plan.Apply (stage, back up, rename, write ledger, rollback on error)
                                      ▼
            manifest saved (desired state) ─▶ old copies dropped ─▶ post-install hooks
```

`internal/engine` runs this pipeline for one scope; `internal/cli` is thin cobra wiring over it.

## Packages

- `paths`: XDG resolution from an injected `getenv`; no globals. `ProjectKey`, the canonical form of
  a project directory that the project lock and `selections.yaml` share.
- `config`: `config.yaml` and `selections.yaml`: strict loading of untrusted, hand-edited YAML,
  selectors, effective exclusions, and locked node-preserving writes.
- `yamldoc`: Reading a hand-edited YAML file as one untrusted document (non-blocking open, regular
  files only, size cap, one document, no anchors, aliases or merge keys) and the node helpers
  `config` and `manifest` edit with.
- `fsx`: Atomic writes, symlink-aware destination resolution, comment-preserving JSONC edits (object
  members, and array elements of any JSON value), mode normalization.
- `module`: The IR, frontmatter parsing, name rules, format detection and the Claude plugin and
  Agent Plugins adapters, loading `lola.yml` (with `components`) + the native layout, hardened
  module JSON reading, rules, hook validation, and normalization.
- `untrusted`: Makes text lola did not write safe to print: control characters, Unicode format
  characters and line separators replaced, length capped, OS errors without their path.
- `source`: Source parsing and validation (also of sources recorded in `project.yml` and
  `lock.json`, `Recorded`), the folder, git and archive fetchers with their executables, the
  reachability check of a pinned commit, and `GitCommand`, the one hardened way lola runs git (also
  for the engine's git-ignore check and migrate's git-tracked guard, with `InWorkTree`).
- `store`: The cache's storage: content-addressed versions with their executables, per-ref heads
  with their commits, `Prepare` (check and digest in the run's staging) and `Commit` (publish under
  the cache lock; `Put` is both), snapshots of a pinned version (`Pinned`), the cache lock.
  (Internal name; user-facing text says "cache".)
- `ledger`, `manifest`: Observed and desired state, validated at load; the ledger written
  deterministically, the manifest edited in place (`manifest.Edit`) with its pins.
- `profile`: Profiles (Plan 6b §4): `Visible` finds the user's profiles and, at user scope,
  `default.yml` by their names alone (at most 256 entries in `profiles/`), and counts the `.yml`
  files whose names fail the module-name rules, which listings report as a count and never by
  name (§19 S7); `Load` reads one through
  `yamldoc.Read` (following a symbolic link, as for `user.yml`, §19 P7), version first, known keys
  only, at most 512 entries each checked as a manifest entry (`manifest.Entry.Validate`), every
  refusal with the file and line; `Entries` gives entries without targets the profile's
  `targets`. No network, no writes; depguard holds its imports.
- `activeprofile`: The qualified names a scope's active profile list stores in `lock.json`
  (`user:go`, `project:go`, `remote:go`; never `default`), at most 64: a leaf with no YAML, so
  `ledger` imports it and `profile` may import `ledger` later without a cycle.
- `trust`: The trust record (`trust.json`): per project key, the view of `project.yml` lola wrote
  and its hash; strict, permission-checked reads, locked per-project saves, coverage, the per-field
  refresh, and `Check`: the status and the typed field changes of a manifest against its record. No
  dependency on `engine`.
- `consent`: Recorded consent to a module's install hooks (key `<module>`) and runtime hooks (key
  `<module>/runtime-hooks/<settings-file hash>`), each recording a digest.
- `target`: The target contract: `Target`, `PlanContext`, `Finding`, capability tables and `Admit`,
  `Probe`, and helpers renderers share. No implementations.
- `target/claude`, `target/gemini`, `target/opencode`: The `claude-code`, `gemini-cli` and
  `opencode` renderers, each with its capability table and probe. One package per target.
- `target/conformance`: Specs every registered target must pass.
- `targets`: The registry: the one list of every target.
- `plan`: Intents, `Compute` (diff + conflicts) and `Apply` (transaction over files, ledger and
  manifest).
- `engine`: Install, uninstall, render, sync (`desired`, the trust report, containment, removals),
  status, fetch, update, search; run staging, module copies, install hooks, runtime hook consent,
  the git-ignore and tracked-ledger checks; every manifest write with its trust refresh
  (`saveManifest`) and the refusals of untrusted entries; install scope resolution, the component
  resolver, selection edits and `InstallChoices`.
- `migrate`: `lola migrate`: strict reading of the Python lola's `installed.yml` and `source.yml`,
  the plan (destinations, outcomes, recorded paths), the trash, and running the installs and the
  cleanup over the engine.
- `cli`: Commands, help, aliases, completion, output and prompts; the install flow and its
  `Prompter`, with the huh adapter.
- `errs`: Errors that carry coaching hints.
- `testenv`: Test isolation (`Isolate()`: fresh `HOME`, XDG dirs, git config, no `LOLA_HOME`, and a
  `PATH` without the developer's assistants) and `ServeGit()`, a loopback git server (`ServeGitWith`
  also shows each request to a spec).

## Selections and the install flow

**Files.** `internal/config` reads `$XDG_CONFIG_HOME/lola/config.yaml` (`default_scope`, `pager`) and
`selections.yaml` once per command. Both are untrusted: at most 1 MiB, one YAML document, no
anchors, aliases or merge keys, known keys only, `version: 1`, module names through `ValidateName`,
known kinds, valid `path.Match` globs (no control or format characters in a selector name, from the
file or a flag), no `include` in `global`, absolute project keys. Errors name the file, line and key
path (keys through `untrusted.Text`) with a hint. Project keys pass through `paths.ProjectKey`
(absolute, clean, symlinks resolved; on Windows forward slashes and a lower drive letter) and
compare with `paths.KeyID` (case-folded on darwin and Windows), so two keys for one project are
refused and a hand-written spelling still matches. The project lock file is named after the same
key.

**Resolver.** `resolveComponents` (engine) is the one place a module becomes the components a
scope installs: the cached version minus the effective exclusions (global skip ∪ scope skip −
scope include) and without rules when they are off. `Install`, `Render` and `Check` plan its
output, so a component skipped after installation is an orphan `plan.Compute` removes (or a
conflict, if hand-edited); `Status` reports the skipped names. A pattern that names nothing is
a warning notice.

**Destinations.** An install acts on an ordered list of destinations: at most one user scope
and any number of project roots. `Scope.Key()` names one: `user`, or the project's canonical
key (`paths.ProjectKey`, compared through `paths.KeyID`), so every spelling of a root is one
destination. Everything the flow keeps per destination is keyed by it: `ModuleChoice.Scope`,
`Preview.Selected`, the flow's assistants, component picks and dropped-pick counts. Each project
root is its own `projects:` section of `selections.yaml`. Results carry `scope` and `root` on
`scopes`, modules and steps. The scope picker's **Other directory…** adds a project root; the
flags still give at most one project and the user scope.

**Scope.** Every command takes the root's persistent `--scope`, `--project`, `--user` and
`-C/--root` (Plan 6b §3.1); `app.scopeRequest` turns them into an `engine.ScopeRequest`.
`Engine.ResolveScope` (`internal/engine/scope.go`) owns the rules: `--user` refuses `--project`
and `-C`; `--scope` (which `--user` and `--project` must agree with), then `--user` or
`--project`, then `-C`, then, on a terminal, `Pick` (the CLI runs the picker); off one, a
`.lola/` found walking up, then `default_scope`, else a refusal naming every way to give a
scope, naming the command (`ScopeRequest.Command`), with `--scope both` only when
`ScopeRequest.Both`. Without `Both`, two scopes from `--scope` or `default_scope` are refused
there too. The CLI's `app.scopes` runs it for `install`'s siblings that write (`uninstall`,
`render`, `sync`); the picker loop (`scopePicker`, which install's flow shares) asks a
one-scope command (`ScopeStep.One`) to tick exactly one, nothing preselected, so an empty
answer asks again. `sync` takes both scopes and syncs them in order, project roots first,
stopping at the first that fails or is declined; `--json` prints `{"scopes": [...]}`, or carries
the synced scopes in the error document (`syncedScopesError`). `Engine.ResolveReadScope` is the read-only rule (`list`): the flags when given,
`--scope both` included, else the project found walking up and user scope, never asking and never
reading `default_scope`; each `StatusRow` carries its `scope`. `check` and shell completions use
`app.scope`: the flags as one scope, else the found project, never asking. `lola sync`'s
completion asks the engine (`SyncNames`): every module the scope's layers want, installed or not
(a profile's included), and every module installed there; it takes no lock and runs no git, so
the tracked-`lock.json` check is left to the command it completes.

**Install.** `InstallAll` takes, per destination, the scope and its source (`flag`, `config`,
`discovered`, `picked`, from `ResolveScope` or the picker), its targets and its selection
edits, plus global edits, and refuses a destination listed twice and any project rooted at the home
directory (it renders where the user scope does, so two ledgers would claim one set of files;
`Render` and `Uninstall` still accept one an older lola installed). Order: the selections lock
(`$XDG_STATE_HOME/lola/locks/selections.lock`, always before any scope lock), then every scope's
lock in `lockOrder` (project roots by canonical key, then the user scope, so two runs never wait on
each other in a cycle), then each scope's module resolution; the edits applied to `selections.yaml`
in memory (`applyEdits`: `--only`, then `--skip`, then `--unskip`); planning through the resolver;
every refusal and prompt for every scope (`prepare`: runtime hook consent, `--strict`,
`plan.Compute` conflicts, install hook consent); `config.Save`; then `commit` per scope. A refusal
or cancel writes nothing; a failure while applying leaves `selections.yaml` written and says the
next `render` converges. `config.Save` re-reads the file under the lock, replaces only the changed
modules' entries through the yaml.v3 node API (comments and order survive, also in a comments-only
or null document; a section or project key a removal empties is dropped unless it carries a comment,
then it stays as an empty mapping), writes atomically at a symlink's target with mode 0600, and
never overwrites a file that does not load.

**Choices and the flow.** `InstallChoices` computes everything the pickers show: per destination
the assistants and the manifest's fixed targets; per cached module (sorted by source, then name)
its components with first-line descriptions, and per destination whether it is installed, what
is excluded and which components are installed now. `Choices.Preview` applies a run's edits to a
copy of the selections with the same `applyEdits`, so the summary shows exactly what the install
will do; `ModuleChoice.Diff` turns that into `+ ~ - =`. `Choices.Versions` and `ModuleChoice.ID`
record the cached versions listed; `installFlow` keeps them as pins (`InstallRequest.Version`)
through every "Change …" step, and `pinnedVersion` stops the run when a pinned version left the
cache. Two arguments resolving to different versions of one module are refused when the choices
are computed. For a module picked from the cache, `listedVersion` lists, per scope, the head of
the ref that scope's entry records (the default branch's for an entry without a ref); a scope
with no entry takes the listed head and records its ref. When no version can be listed (the
entry's ref has no cached head, a ref-less entry whose default branch is not cached, scopes
resolving to two versions, a git head cached before pins with no commit for `pinCommit`) the
module is left out of `Choices.Modules` and `Choices.Warnings` names the command that installs
it. A kept pick that `listedVersion` no longer returns is refused while the choices are
computed, before the summary, as is a module argument whose git version `pinCommit` could not
pin. The CLI's `installFlow` only fills answers: each step asks the `Prompter` (one method per
step and one for the review menu) unless a flag or the situation answers it, component picks
become `--skip`/`--unskip` edits, and the equivalent commands are built from the same spec.
`Prompter` implementations: the huh adapter (inline forms on stderr; accessible line prompts
under `LOLA_ACCESSIBLE=1` or `TERM=dumb`, with an `eofReader` that turns end of input into a
cancel), the non-interactive one, and the specs' scripted one. In accessible mode every prompt,
hook consent included, reads through the `eofReader`, so end of input is `ErrAborted`. Specs
parse the equivalent commands back through the real command tree and compare the plans.

## Plans and the ledger

Targets return **intents** (`WriteFile`, `CopyTree`, `SetJSON`, `AddJSONMember`).
`plan.Compute` expands trees, then, in two passes:

1. **Desired.** Each intent's destination is read at its symlink-resolved location and
   classified: new → create; identical → adopt (recorded, not rewritten); lola's and unchanged
   → update; lola's but hand-edited, or not lola's and different → conflict; owned by a module
   that is not being replanned, or claimed twice → non-forceable conflict. Two logical
   destinations that resolve to the same real file (symlink aliasing) are refused. A file whose
   bytes match but whose normalized mode differs is a **mode-only update** (noted as
   `mode changed`), not an adoption.
2. **Orphans.** Ledger artifacts nobody claimed are deleted if unchanged, kept as conflicts if
   hand-edited, forgotten if already gone. Running desires first lets a destination move between
   modules in one operation without a spurious conflict or delete.

The result is byte-exact `FileChange`s and the ledger that applying them produces. A dry run is
exactly this computation. Each ledger entry records the targets its module is installed on
(`ledger.Entry.Targets`, `targets` in `lock.json`), since a target can render none of a module's
files and still have run its hooks: `updateLedger` keeps the entry's targets outside the change's,
adds the change's entry targets (`ModuleChange.Entry.Targets`, the engine's `ModuleResult.Targets`)
and every target it writes an artifact on, and drops the entry once no target is left.
`ledger.Load` refuses targets that are not target names, are listed twice, or omit an artifact's
target. An entry from before the field falls back to its artifacts' targets
(`Entry.InstalledTargets`).

**Profiles in the ledger** (Plan 6b §5). `profiles` is the scope's active list, in order: each
item a qualified name (`user:go`, `project:go`, `remote:go`; never `default`) and the action that
put it there (`by`: `on` or `switch`), at most 64, each listed once. A module's `from` names the
profile whose entry won when lola last applied it (`profile:user:go`, `profile:default`); an
entry the scope's manifest won has none. Both keys are left out when empty, so a ledger without
profiles is written byte for byte as before them, and the version stays 1. `ledger.Load` refuses
a list or a `from` lola never writes.

**Replaceable paths.** `Request.Replace` (from `ScopeInstall.Replace`) lists unowned
destinations the caller lets the plan overwrite instead of refusing: a file, every file below a
directory, or a JSON member at one pointer. Only the "exists and was not written by lola" case
changes: the step is an update marked `Replaced`. Ownership still wins (a module's path, or its
own hand-edited file, stays a conflict), and a path with a symbolic link between the target's
root and it (`fsx.Linked`) is never replaceable. lola migrate passes the Python lola's recorded
paths after its guards, and backs up exactly the steps marked replaced.

Hashes: files are hashed by bytes, JSON members by their canonical JSON (sorted keys, no
comments). JSON edits operate on the hujson AST so bytes outside the edited member are
preserved; trailing line comments belong to the line they end. `ledger.created` remembers the
JSON files lola created, so they are deleted when lola's last member leaves them empty.

**Owned array members.** `AddJSONMember` claims one element, any JSON value, in a JSON array that
users also edit: a string for rule references (Gemini and OpenCode), an object for a runtime hook's
matcher group (Claude Code). Elements compare as canonical JSON, so key order and layout in the
user's file do not matter. The ledger records a string as `value`, as it always has, and any other
element as `member`, in canonical JSON; either is part of the artifact's identity, so several
modules can each own an entry in one array, and the hash is over the element's canonical JSON. Files
and step lines get the element with `& < >` unescaped (`fsx.ReadableJSON`), so a shell operator
reads as one. A ledger `member` that another tool reformatted is recorded again as canonical JSON at
load; one that is a string or `null` is refused, with a hint. A member is present or absent, never
hand-edited: one the user removed is rewritten with a note, and an orphan the user removed (or
edited, which changes its identity) is forgotten. A string the user already listed is adopted; any
other element the user already has is a forceable conflict, since owning the user's own hook group
would delete it on uninstall. A `created` entry with a `key` records an array lola created; when
lola's last member leaves it, the array goes too, with the parent objects it leaves empty, and the
file if lola created that. A value at the pointer that is not an array is a hard error. Appending
keeps comments that follow the last member before the closing bracket or brace, so removing lola's
entry restores the file byte for byte.

**Rules** are plain files each target writes where its assistant loads them, never in a
user-owned instruction file. Claude Code loads `.claude/rules/lola/<module>/<rule>.md` as is.
Gemini CLI has no rules directory: a module's rules are joined into
`.gemini/lola/<module>/GEMINI.md`, and that directory is an owned member of
`context.includeDirectories` in `.gemini/settings.json` (Gemini walks it for `GEMINI.md` in
trusted folders; observed with 0.63.0). OpenCode gets one file per rule in
`.opencode/lola/<module>/` and one owned glob member in `instructions`. At user scope the members
are absolute, because user settings apply in every project; OpenCode expands a wildcard only in
the last component of an absolute entry, hence one member per module. Every artifact belongs to
exactly one module.

## Targets

The design is `docs/superpowers/specs/2026-10-09-target-framework-design.md`.

- **One package per target** (`internal/target/claude`, …) implements the contract in
  `internal/target`; `internal/targets` lists them explicitly (no `init()` registration). A
  `depguard` rule in `.golangci.yml` lets target packages import only the contract (exactly, not
  a sibling target), `module`, `paths`, `plan`, the standard library and their vetted
  third-party packages (`yaml`, `toml`, `semver`), never the engine, the CLI or lola's state.
- **Scope and root.** `Detect(scope, root)` gets the target's root for the scope, and
  `PlanContext.Root` carries it into `Plan`. OpenCode needs both: its user-scope root is its own
  config directory, whose existence marks it as set up, and it writes to `opencode.jsonc` when
  the root has one, else `opencode.json`. Targets may read the disk this way; they never write.
- **Findings, not errors.** A target returns what it loses as `target.Finding`s with a severity:
  info, warn, or skip (that artifact is left out for that target). `Plan`'s error is only for
  failures that are not about module content. `target.Skipped` applies `--force` (forceable
  skips are rendered, marked `Forced`); `engine.StrictError` decides what `--strict` refuses.
  Load warnings become findings about the module; the engine's own messages stay `Notice`s.
- **Capability tables.** Each target declares `Capabilities`: supported artifact kinds,
  frontmatter keys with value shapes and patterns (or always dropped), required keys, name
  patterns, and content rules. `target.Admit` checks one artifact against them, returns the
  document without the dropped keys, and warns about YAML values that 1.1 and 1.2 parsers read
  differently (`module.Doc.Ambiguous`). Renderers render through `Admit`, so the table is both
  what lola checks and what it does. Value-dependent checks stay in the target's code and
  return the same findings.
- **Probes.** `Detect` combines a binary lookup over the absolute `PATH` entries
  (`paths.Paths.Search`; relative entries are ignored, so a repository cannot plant a binary)
  with the target's project markers, and executes nothing. Install, render and check run each
  participating assistant once with its version arguments (`Engine.ProbeTimeout`, no shell),
  reading at most 4 KiB of its output, cached per binary build in
  `$XDG_CACHE_HOME/lola/probes.json`. The version only feeds
  `PlanContext.Version`, which `Admit` compares with capability `Bounds` to add findings;
  rendering always follows the newest version, so output is the same on every machine.
- **`lola check`** (`engine.Check`) stages a source like `cache add` into a temporary directory
  and detects its format (or reads a cached module), runs every target's `Plan` and keeps only
  the findings.

## Module formats

Detection (`module.Detect`) runs only on fetched content: `Store.Put` (cache add, install of a
source, update) and `engine.Check` of a source. It walks ADR #243's precedence: a lola
manifest, else the first plugin manifest an adapter accepts (root `plugin.json` with the Agent
Plugins `$schema`, then `.claude-plugin/plugin.json`), else the native layout. Every
lower-precedence manifest present is recorded in `Module.Shadowed` with a warning; an adapter
that finds the file is not its format declines with a warning and detection moves on.

- **Adapters run once and are written down.** An adapter (`module.adapter`: a manifest path
  and a `read` that returns a `lolaFile`) converts the plugin manifest into the same structure
  a `lola.yml` decodes into: metadata, `format`, a `components` section that lists every kind
  explicitly (an empty list turns discovery off) and `formats.<format>` for data lola does not
  use. That structure is applied and checked by exactly the code that reads a `lola.yml`, and
  `Normalize` writes it as the cached `lola.yml`, next to the plugin's untouched files.
- **The load path is format-free.** `module.Load` is detection with no adapters, and it is how
  the cache's versions are read: the generated `lola.yml` wins precedence, and its
  `components` make discovery irrelevant, so the plugin manifests left in the cache are inert
  and a lola upgrade cannot reinterpret a cached version. A spec loads each format's cached
  copy this way and checks that every target renders it exactly like the fetched one, and that
  normalizing it again changes nothing. A cached load does not see what the adapter warned
  about, so `Store.Put` returns the version with detection's warnings.
- **`components`** (`internal/module/components.go`): per kind, path or inline entries with
  optional per-entry `targets`. Paths are checked element by element with `Lstat` (local,
  existing, no symlink); names follow `ValidateName`; two artifacts of a kind and name whose
  effective targets overlap are refused. Targets honor per-entry `targets` for every kind,
  rules included. Runtime hooks (`Module.RuntimeHooks`, canonical JSON of the event map) are
  checked down to the handler fields lola rewrites or shows; only `claude-code` lists the
  `runtime-hook` kind (see [Runtime hooks](#runtime-hooks)).
- **Module JSON** (`mcps.json`, `plugin.json`, `.mcp.json`, `mcp.json`, hooks files) is read by
  one helper: a regular file, not a symlink, at most 1 MiB, nesting at most 64 levels, and no
  object key repeated at any depth (a token walk before decoding, naming the key's JSON
  pointer).
- **Defense in depth.** The planner refuses one module claiming a destination twice ("provided
  twice by module X") rather than letting the later intent win.

## Module copies

`${PLUGIN_ROOT}` expands to the module copy, `.lola/modules/<module>/` (user scope:
`$XDG_DATA_HOME/lola/user-modules/<module>/`), never into the cache. The path does not change
between versions, so rendered files stay valid while a copy is replaced. Targets read artifact
content from the cache version (`PlanContext.ContentDir`) and expand `${PLUGIN_ROOT}` to the
copy (`PlanContext.PluginRoot`), because the copy is only put in place after the plan is
computed.

At project scope the engine refuses to run when `.lola/`, `.lola/modules/` or
`.lola/plugin-data/` is a symlink (`refuseSymlinkedState`, before the lock is taken, also for
dry runs and `status`): a repository can commit one, and the copy swap would then rename and
delete directories outside the project, or plugin data directories would be created there. The
same check refuses a symlinked `.lola/project.yml`, which the manifest write would otherwise
replace outside the project. User-scope copies and `user.yml` may live behind a symlink;
`manifest.Edit` then writes at the link's target.

Install hooks build inside copies (`npm install`, virtual environments), so a copy's content is
never compared with the cache. `render` and `sync` replace a copy only when its module's version
changes (the ledger digest differs, or there is no ledger entry), the copy is missing
(`needsCopy`), or the module gains a target the ledger does not record it on (`addsTargets`), whose
hooks never ran; an entry that is not a real directory, such as a committed symlink, counts as
missing. `install` plans every copy as replaced from the hashed version, so its hooks run only
in bytes the consent was given for; a copy that holds that version already, for an install
that adds no target to those the ledger records, is planned forced (below) and kept when no hook
runs. A target the module is not installed on has never had its hooks run, so an install, sync
or render that adds one replaces the copy and declines, or refuses, hooks without consent. Each replaced copy is
assembled as `.<module>.new`, the current one moves to `.<module>.old`, and the new one takes its
place. If the ledger is never committed (the `after` callback of `plan.Apply` runs only after the
commit), the old copies are restored; otherwise they are dropped. Copies of modules that leave the
ledger are removed. `status` reports a missing copy as `missing`, and `render` restores it and
reruns its hooks; edits inside a copy are not reported.

## Install hooks

`pre-install` and `post-install` scripts must be executable regular files inside the module
(checked when the module is loaded). After the plan is computed and before anything is written,
the engine decides consent per module: `--allow-hooks` (this run only), a recorded consent for
the same digest (sha256 over each hook's event, script path and script sha256), or the CLI's
prompt, whose yes is recorded in `$XDG_STATE_HOME/lola/consent.json`. Without consent a
`pre-install` hook is an error and a `post-install` hook is declined with a warning.

Hooks run once per target, in the module copy, with the parent environment plus `LOLA_MODULE_NAME`,
`LOLA_MODULE_PATH`, `LOLA_PROJECT_PATH` (the target's scope root), `LOLA_ASSISTANT`, `LOLA_SCOPE`
and `LOLA_HOOK`, under a 10-minute timeout. A failing `pre-install` hook restores the old copies and
writes nothing; `post-install` hooks run after the commit, so a failure is a warning. `install` runs
hooks for every module it installs that has consent (a module whose copy it keeps, not pending,
asks nothing off a terminal and is not refused for a `pre-install` hook); `render` only for modules
whose copy it replaces (a new version, or a missing copy). A dry run reports hooks as `would-run`
(`would run` in human output).

A `post-install` hook declined for want of consent is **pending**: `prepare` sets
`ledger.Entry.HooksPending` for a module whose post-install hook it declined, clears it for one
whose hooks run, and keeps it for a module whose hooks the run does not reach. Consent is keyed
on the scripts of the hashed version (`hookScripts(c.src, …)`) while hooks run in the module copy,
which `needsCopy` never compares, so a hook must never run in the kept copy: `Sync` plans a
pending module's copy as replaced (`moduleCopy.forced`), swapped in again from the snapshot or
staged tree, and `Install` plans every copy that needs no new version and adds no target the
same way; their hooks
go through `planHooks`. When no hook job is planned for a forced copy, `keepUnhooked` undoes the
replace before `commit`, so the copy, what earlier hooks built in it and its mark stay (a sync is
still in sync). A forced copy whose hook is not pending (`moduleCopy.pending`) is skipped
without a notice or a `pre-install` refusal when consent is missing. A planted
`hooks_pending` in an untracked `lock.json` therefore runs nothing the user did not consent to
for those bytes. The declined-hook notice names `lola sync <m>`, and off a terminal
`lola sync <m> --allow-hooks`, with the scope's flags. A pending hook is offered only to a run
that names its module or gives `--allow-hooks` (`planSync`'s `named`): any other run plans the
copy as it is, asks nothing, and adds the notice `pending post-install hook for <m> → lola sync
<m>`, so an unrelated full sync never stops at the hook question.

## Runtime hooks

`target/claude` renders each matcher group of a module's runtime hooks as one `AddJSONMember` intent
(kind `target.KindRuntimeHook`) under `/hooks/<Event>` of `.claude/settings.local.json`, or
`~/.claude/settings.json` at user scope. Command handlers get `CLAUDE_PLUGIN_ROOT` and
`CLAUDE_PLUGIN_DATA` (`PlanContext.PluginRoot` and `PluginData`, the same values `target.Expand`
uses): textual forms are replaced in one pass, shell form gains an `export` prefix, and exec form
becomes `env -- NAME=value NAME=value <command> <args>`. `--` must come before the assignments: GNU
`env` reads a `--` after them as the command. Paths that are not shell-safe, commands `env` would
misread, PowerShell handlers and a settings file git could commit are forceable skips of the whole
set; forced renders export single-quoted values and refer to the variables, so a POSIX shell never
parses a lola path as shell syntax (an unquoted reference is still split at spaces and
glob-expanded, so the docs tell authors to quote it). A path holding `' " $` `` ` `` or `\` is never
forced, since the forced form relies on those characters' quoting rules. An event that is not an
identifier is an unforceable skip. On Windows only exec form fits: every `C:\` path holds `\`, so
shell-form hooks are never forced there, and the skip's hint names exec form alone.

**Git-ignore check.** Targets stay pure, so the engine computes
`PlanContext.LocalSettingsExposed` once per command (`probeRun.localSettings`), at project scope
and only for modules with runtime hooks: if the project or a parent holds `.git`, it runs
`git ls-files --error-unmatch` (status 1 means untracked) and `git check-ignore -q --no-index`
(status 0 means ignored) through `source.GitCommand`, whose `-c` overrides keep a cloned
repository's config from running programs (fsmonitor, hooks). Any other status, or no git, sets
the reason, and the target skips the hooks (fail closed).

**Frontmatter hooks.** Claude Code also runs a `hooks:` key in skill, command and agent frontmatter.
`module` checks every key equal to `hooks` ignoring case like `runtime_hooks`; `target/claude`
(`componentHooks`) gives their command handlers the same rewrite and renders the rest of the
document through `target.Expand` separately, so `Expand`'s raw substitution never reaches hook shell
text. Two load refusals close the ways around this: hooks in a `.md` file under a command's sidecar
directory (Claude Code reads it as a command; lola copies it as a file), and a first line starting
with `---` that `ParseDoc` does not read as frontmatter (more dashes, trailing whitespace or text,
no line break), where another parser might find hooks. `allowed-tools` is installed as written, with
the warning `claude-code/allowed-tools`.

**Consent.** Invariant: every intent of kind `target.KindRuntimeHook`, every intent of a component
carrying hooks, and every intent of an agent with inline MCP servers (Claude Code starts them with
the agent) passes the consent gate. Such intents carry a `plan.HookConsent` naming the settings file
whose consent covers them (and, for a component, its kind, name, hooks and, for an agent, its inline
servers as rendered); an intent without one is refused if it has the kind `KindRuntimeHook` or,
whatever its kind, edits `/hooks` in a `.claude/settings.json` or `settings.local.json`. In
`execute`, before `--strict` and before `plan.Compute`, the engine groups a module's gated intents
by settings file and computes their digest: sha256 over each group's target, pointer and canonical
JSON, and each component's target, kind, name, canonical hooks and canonical inline servers (all as
rendered, so absolute paths are in it), then the module version's content digest. Consent is
recorded in `consent.json` under `<module>/runtime-hooks/<sha256 of the settings file's absolute
path, symlinks resolved>`, which cannot collide with an install hook entry because module names have
no `/`, and gives each project and scope its own consent. A recorded digest, `--allow-runtime-hooks`
or a yes from the CLI's prompt keeps the intents (the latter two record the digest); a dry run makes
the same decision without asking or recording; otherwise the intents are dropped with
`<target>/runtime-hooks-declined`, per settings hooks and per component. Dropping them is what makes
consent fail closed: the planner sees the module's installed groups and components as orphans and
removes them. The consent view (`engine.RuntimeHook`) reads handler fields by exact name, as Claude
Code does, shows every handler or matcher-group field it does not describe as JSON, and passes every
string through `untrusted.Text`. An inline server's view entry has `mcp_server` set and no event; it
shows env and header values, since they change what runs.

**Drift.** A hook group is identified by its content, so an owned group missing from its settings
file is one the user edited or removed. `plan.Compute` keeps a missing owned non-string member owned
but absent (step `absent`) instead of writing it again, which would run both copies; the engine
reports `<target>/runtime-hook-drift` as info with a hint (it records the user's choice, so
`--strict` ignores it, and human output prints info findings that carry a hint), and `--force`
writes it again. On uninstall the member is forgotten and the drift is info. String members and
files keep the "was missing; rewritten" rule.

## Applying

`plan.Apply` runs under the scope lock: `$XDG_STATE_HOME/lola/locks/<hash>.lock` for a project,
named after the resolved `.lola/` path, or `$XDG_STATE_HOME/lola/.lock` at user scope. The lock
lives outside `.lola/`, so removing an empty `.lola/` never unlinks a lock another process is
waiting on. Apply stages every new file next to its destination, moves each existing destination
aside, renames the staged files in, writes the ledger, and then, when the command changes it,
the manifest: staged next to it (at a symlink's target), the current one backed up with a hard
link (a copy where links fail) and left in place, the new one renamed over it, and the directory
synced, so a crash never leaves the scope without a manifest. The ledger and manifest writes are
part of the transaction: any failure up to and including them restores files, ledger and
manifest in reverse order. Afterwards backups are removed, emptied directories are pruned (never
past a target's base directories), and a logical symlink whose target was deleted is removed;
the `after` callback then runs.

The manifest's bytes come from `manifest.Edit`, run under the scope lock before `Apply`: it
reads the file again (refusing one that does not load) and edits the yaml.v3 node tree, touching
only the entries and fields the command changes and appending new entries, so comments and order
survive. It decodes and validates the bytes it produces as `manifest.Load` would, so nothing it
could not load again (a commit from a corrupt cache index, say) is written. The file is encoded
again (yaml.v3, two-space indent), so blank lines between entries are dropped and indentation is
normalized. `manifest.Load` and `ledger.Load` check every recorded source, ref, path, commit and
digest (`source.Recorded`) and refuse text that would not print as it is (`untrusted.IsPlain`),
before anything reaches git; `manifest.Load` reads through `yamldoc.Read`, the reader
`config.yaml` and `selections.yaml` use (non-blocking open, regular files only, 1 MiB, one
document, no anchors, aliases or merge keys), and reads `version` before the strict decode.

`.lola/` itself is created when the scope lock is taken; its `.gitignore`, which lola owns, is
written when a scope is committed, so a refusal or cancel never touches a `.lola/` that
already existed (the Python lola's, for example). On
release `.lola/` is removed only if this command created it and nothing was recorded (no
`project.yml`, and an empty or absent ledger), so a failed or empty first run leaves no trace.

## Fetching sources

`source.Parse` classifies an argument; `source.Fetch` stages a module from a `Spec`. Both call one
unexported `validate`, because the engine also builds `Spec`s from the ledger and the cache index
without parsing an argument, and a project's files may be someone else's. `validate` refuses
`-`-prefixed locations and refs, remote-helper syntax, `git://`, credentials, queries and fragments
in URLs (`file://` included), plain `http` off loopback, and refs that fail `git check-ref-format`
(full SHAs skip that check). A user name is allowed only in `ssh://` and scp form, a password never
is, and an scp location with `user:password@`, `?` or `#` is refused. A local archive must be a
regular file, checked with `Lstat` before any open, so a pipe cannot block lola.

```
git      → clone into scratch ──────────── listing: git ls-files in the clone
archive  → extract into scratch ─────────┐
URL      → download → extract ───────────┴ listing: directory walk (never git)
folder   → (in place) ──────────────────── listing: git ls-files in a work tree, else a walk
            ↓
         module root → copy (ignore list, no symlinks) → Fetched{Dir, Name, Commit, Executables}
```

**Run staging.** Every engine operation that fetches (`InstallAll`, `InstallChoices`, `Fetch`
for `cache add` and Add a source…, `Update` and `Render`; `migrate` goes through `InstallAll`)
creates its own `run-*` under `$XDG_CACHE_HOME/lola/.staging/` (mode `0700`, `Engine.runDir`)
and removes it on every return path. All but `Fetch`, which reports no notices, first remove
`run-*` directories last modified more than a day ago (`Engine.sweepRuns`); the sweep is best
effort, and what it cannot remove is a warning notice, never a refusal. Everything
temporary goes inside: fetch scratch (`scratch-*`), fetched copies (`fetch-*`), the
reachability check's repository, version builds (`version-*`, assembled beside the staged tree
by `Store.Put`) and snapshots. A process killed outside lola's control leaves its directory
until the next sweep. The sweep goes by the run directory's modification time, which changes only
when a direct child is created or removed, so a run idle for more than a day (realistically, a
command left waiting at a confirmation prompt; a fetch that long inside `scratch-*` is the
other case) can be swept; the user docs say so. A dry run sweeps too, since the directories are
lola's own temporaries and the sweep precedes the dry-run check (`InstallAll`, `Render`).

Each remote kind produces a local tree in a scratch directory in the run's directory, which is
removed on success and on error. The copy that every kind shares then runs over the
tree with an explicit **listing mode**: it never guesses for a fetched tree, so a `.git/`
inside an archive is never handed to git, and an archive extracted below the user's own
repository is never filtered by that repository's ignore rules.

- **git.** Every git command starts with `-c core.hooksPath=/dev/null -c
  protocol.ext.allow=never -c core.fsmonitor=false -c core.autocrlf=false` and empty LFS
  filters; user input appears only after `--` or as `--branch=<ref>`. Clones use
  `--no-checkout`; before any checkout lola writes `$GIT_DIR/info/attributes` (`* -text -eol
  -filter -ident -working-tree-encoding`, which outranks `.gitattributes`) and lists the tree
  with `ls-tree -r -z`: a mode `120000` entry inside the module path (`--path`, or the whole
  tree without one) or on the way to it is refused (Windows without `core.symlinks` would check
  it out as a plain file; links elsewhere never reach the module), as is a `--path` that is no
  directory of the tree in its exact letter case (a case-insensitive checkout would find it),
  and `100755` entries are the version's executables. Then `reset --hard` (or
  `checkout --detach` for a SHA) writes the bytes as committed. The environment sets
  `GIT_TERMINAL_PROMPT=0` and `GIT_PROTOCOL_FROM_USER=0` and drops the variables that select or
  configure a repository (`GIT_DIR`, `GIT_CONFIG_PARAMETERS`, …); the folder fetcher's
  `git ls-files` uses the same prefix and environment. A ref resolves through `git ls-remote`
  with full ref names, matched exactly. A full SHA that names no branch or tag is fetched with
  `init --object-format`, `fetch --depth 1` and `checkout --detach`; clones run with `-q`, so a
  failure shows git's errors, not its progress. One 5-minute context covers the fetch;
  `WaitDelay` (5 s) makes git stop on time even when a child keeps its output open, and a
  timeout is coached by the location's transport (ssh prompts, or the network for https). git's
  stderr, which carries a remote's `remote:` lines, goes to a `headWriter` that keeps only its
  first 4 KiB and discards the rest, so a chatty or hostile remote cannot grow lola's memory.
- **Archives.** Extraction writes through an `os.Root` opened on the scratch directory, so no
  entry resolves outside it even if a name check has a bug. Only regular files and directories
  are created, files with `O_EXCL`; every entry is copied through a reader capped at its
  declared size plus one byte. The entry, byte and ratio limits are constants that specs lower
  through an unexported parameter. Before `archive/zip` parses a zip's central directory into
  memory, `checkZipDirectory` finds the end record (and the zip64 one) the way `archive/zip`
  does and refuses a declared count over the entry limit, or a declared size or actual span
  over 1 KiB per allowed entry: `archive/zip` reads records until one fails to parse, whatever
  the declared size, and checks the count only modulo 65536.
- **Downloads.** A clone of the default transport (proxies honored) with compression disabled, a
  2-minute timeout, at most 5 redirects, none from https to http and none to http off loopback, and
  a body capped at 256 MiB.
- **Untrusted text.** git's stderr, archive entry names, module-root candidates, HTTP status text
  and echoed URLs go through `untrusted.Text`, which replaces control characters, invalid UTF-8,
  Unicode format characters (bidi overrides, zero-width characters, BOM) and line or paragraph
  separators with `?`, indents continuation lines, and caps the length. A parsed URL is echoed as
  scheme, host and path only (`displayURL`); one that does not parse, or was never checked, is
  echoed up to its first `?` or `#`, and only as its scheme when it holds an `@` (`redactURL`). The
  redirect target of a failed download and refused `git://` URLs are not echoed at all; OS errors
  lose their path.

Every fetch reports its **executables** from the source's own metadata: git's tree modes, an
archive's entry modes, a folder's file system. The digest takes execute bits from that list,
never from the file system lola copied to, so a platform without execute bits digests the same
content the same.

**Reachability.** A pinned git commit must be reachable from the entry's ref:
`source.CheckReachable` resolves the ref as a fetch does, fetches its history with
`--filter=tree:0` into a separate, freshly initialized scratch repository with
`GIT_NO_LAZY_FETCH=1`, and asks `git merge-base --is-ancestor`; a commit that is absent or not an
ancestor is refused. It is skipped when the ref is the commit itself.

## Cache

```
$XDG_CACHE_HOME/lola/modules/<name>/
  index.yml                # source, type, path, heads: [{ref, digest, commit}]
  versions/<digest12>/
    content/               # the normalized module: native layout + one generated lola.yml
    .lola-origin           # format, manifest, others_present, ref, commit, fetched_at,
                           #   digest, executables
```

The digest is sha256 over, for each regular file under `content/` in byte order of its slash
path, `path NUL octal(mode) NUL decimal(length) NUL content`, where mode is `0755` for a path in
the version's `executables` and `0644` otherwise (the generated `lola.yml` never is). A change to
its input, or to the bytes a fetch and `module.Normalize` produce for unchanged content, bumps
`manifest.Version`; a golden-digest spec guards it. A head records the commit its fetch
resolved; `.lola-origin`'s `commit` is only the first fetch's, which may be another commit with
the same content. Putting content the cache already holds rewrites the version's `executables` when
they differ, so a version cached before execute bits were recorded is repaired by the next fetch
of it (`cache update`, or `install` of its source).

`cache add` detects the staged module's format and reads it (`module.Detect`), normalizes it
(`module.Normalize`: one generated `lola.yml`, every other lola manifest removed), digests the
result, and renames the version into place in whole from the run's staging directory under
`$XDG_CACHE_HOME/lola/.staging/` (same filesystem), under `$XDG_CACHE_HOME/lola/.lock`.
`.lola-origin` records the detected format and which manifests were read and ignored. Module
names and version ids read back from the cache are validated before they are used in a path; a
head's commit must be empty or a full SHA, and a version's recorded digest must be the one its
id abbreviates, or the cache coaches `lola cache rm`. The IR is re-derived from `content/`,
never stored.

Installs run from their module copies, so the cache is re-fetchable. `render` installs the version
the ledger records, never a head, so `cache update` changes no installation: for a git or archive
module `Store.Pinned` copies that version's `content/` into a **snapshot** in the command's private
`run-*` staging directory, hashes the copy with the recorded executables, and loads the module from
it, so the IR, the intents and the module copy all come from the bytes that were hashed. On a miss
(after `lola cache rm`, say) `fetchPin` fetches it again from the cache's recorded source or the
ledger's: a git commit must be reachable from the ref and is fetched by SHA; the content is prepared
in the run's staging (`Store.Prepare`) and must declare the module's name (`PutRequest.Name`) and
have the recorded digest, or nothing is cached; then render commits it (`Store.Commit` with
`headIfAbsent`, so the head for the ref is set only if it has none) and renders from the staged tree
it hashed. A folder module is read from its folder (the version installed when the folder is gone),
and refused, with nothing cached, when it now declares another name. When the version cannot be
obtained (a git entry with no recorded commit, a fetch that serves another commit, other content
than the recorded digest) the error carries two hints: `lola install <source> [--ref r] [--path p]`
(`installCommand`), which installs what the source serves now, and `lola uninstall <name>`.
`install` pins git and archive entries (`commit` from the fetch or head, `digest`); folders are
never pinned. `status` reports `stale` when the manifest's pin differs from the ledger's digest, and
`update_available` when the cache head for the entry's ref differs from the pin (or from the
ledger's digest when unpinned).

## Trust record

`project.yml` is committed and someone else's after a pull, so lola keeps its own account of what it
wrote there. The **view** of a manifest is what can change what lola fetches or writes: the
top-level `targets` and each entry's `name`, `source`, `ref`, `path`, `commit`, `digest`, `targets`
and `rules`, modules sorted by name, empty values omitted. `trust.json` (`$XDG_STATE_HOME/lola/`)
maps each project key (`paths.ProjectKey`, compared with `SameKey`) to its view and the hex sha256
of the view's canonical JSON. An entry is **covered** when the record holds an entry of its name
with every view field equal. Top-level `targets` are recorded; `lola sync` shows a change to them
(`trust.Check`), an install refuses to take uncovered ones unasked (`untrustedTargets`, set per
scope in `InstallAll` when no `-t` is given; the pickers get them as
`ScopeChoices.UntrustedTargets` and ask instead), and `render` leaves out a module that installs
to them. A project with no record covers nothing: there is no seeding from `lock.json` (user
ruling, 2026-10-13: lola-go is unreleased, so no older project needs it). User scope has no
record; `user.yml` is the user's own, and the engine never loads one for it.

- **Reading.** The version is decoded on its own first, as `manifest.Load` reads `project.yml`'s: a
  newer one is refused with the hint `upgrade lola` (not for a key the newer lola added) and a
  negative one as unreadable. Then strict JSON with unknown keys and trailing data refused, at most
  4 MiB, and each record's hash checked against its view. A file that fails is an error with a hint
  and is never overwritten. The file and `$XDG_STATE_HOME/lola` are refused when a symbolic link or
  (outside Windows, whose permission bits mean nothing) group- or world-writable; the opened file
  must be the one checked (`os.SameFile`). Go reports a Windows junction as irregular, so a state
  directory that is one is refused as not a directory. A version up to this lola's is read, 0
  included, as `consent.json` reads its own.
- **Writing.** Under `$XDG_STATE_HOME/lola/locks/trust.lock`, taken after every scope lock and
  held only around the read-modify-write: the file is read again and only this project's record
  replaced, then written `0600` through `fsx.WriteFileAtomic`. The state directory, `locks/` and
  the lock file get the read checks before the lock file is created, and a save that would make
  the file larger than the 4 MiB a read accepts is refused, which leaves the file as it was.
- **Refresh.** Every manifest write goes through `engine.saveManifest(sc, loaded, change, rec,
  apply)`: it edits the manifest, hands the edit to `apply` (the commit, which stages it with
  the ledger), and after a successful apply at project scope saves `trust.Refresh(record,
  view(loaded), view(loaded.Applied(change)))`. Every field the write changed takes its new
  value, an added entry is recorded whole, a removed one is dropped, and every other field keeps
  its trust state: a write never covers a field it left alone, and a field changed in an entry
  the record does not hold is not recorded. `loaded` is a clone of the manifest read under the
  scope lock. A failed save leaves lola's own edits uncovered (fail closed) and the error says
  what the caller applied (an uninstall says it removed what was asked), and which scopes of the
  same install it then did not reach.
- **Accept.** A sync records what the user accepted with `trust.Accept(record, view(loaded),
  modules, targets)`: the entries named take their view in the loaded manifest (an entry the view
  lacks is dropped), the top-level targets when `targets` is set, and every other entry keeps its
  record. Accepting every change and the targets returns the view itself, which is what an
  all-or-nothing answer records; a partial answer, or a run limited to some modules, names fewer.
  With pins to write, `saveManifest` refreshes `&Record{View: Accept(…)}`, so only the accepted
  entries' pin fields move.
- **Refusals.** Over an uncovered entry: an install by name (in `InstallAll` and `InstallChoices`,
  checked for every argument at every scope before any is resolved, so nothing is fetched), an
  install that adds targets (`lola migrate`'s), and an uninstall that keeps some of the module's
  targets are refused (`notTrusted`, wrapping `ErrNotTrusted`; the message, from `untrustedWhy`,
  says "changed for it since you last trusted it", "added it since you last trusted the file" for
  an entry the record never held, or "has not been trusted on this machine" for a project without
  a record) with the hint `review and apply it: lola sync <m>`. 6a.2 and 6a.3a hinted installing
  from the source, which re-pinned to what the source served and skipped sync's review; that
  helper is gone. An install by source writes the entry as new: it
  inherits no `targets`, `rules`, `ref` or `path`, and `planInstallModule` drops the entry from
  `loaded`, so the refresh records it whole. With no targets given, the module keeps the targets
  `lock.json` installed it on (the user's own installs chose them; a ledger git tracks is refused,
  see below), so what is installed is what the entry records. An argument that names an uncovered
  entry is refused as a name even when `source.Parse` would take it as a folder: `resolve` takes a
  cached name first and applies the entry's ref to any name-shaped argument, and the pickers' fetch
  caches the folder under that name, so only `./<name>` is unambiguous. Removing a module is always
  allowed. The pickers never offer what the install would refuse after the summary: an explicit name
  is refused at listing, a cached module with an uncovered entry is left out under one notice per
  project scope (`uncoveredEntries`), and
  `moduleScope` shows a source over an uncovered entry as the new entry it becomes. Add a source…
  caches the source and lists it by name, so an uncovered entry stays out of the pickers (S2 applies
  only to a source argument), and Change scope onto a scope where a kept pick is uncovered is the
  refusal, not a dropped pick. `lola migrate` reports the module as unmappable with the refusal's
  reason and hint; it refuses after `resolve` has fetched the source, which is the user's own legacy
  configuration.
- **Tracked ledger.** At project scope every operation that decides from the ledger runs
  `refuseTrackedLedger` after loading the scope and before resolving or fetching anything:
  `Render`, `InstallAll`, `InstallChoices` (so the pickers refuse what the install would) and
  `Uninstall`; `Status` (`lola list`) only reads and does not. It runs `git ls-files
  --error-unmatch -- .lola/lock.json` through `GitCommand` (`ledgerTracked`): tracked is refused
  (hint `git rm --cached .lola/lock.json`), a git failure in a work tree is refused (fail closed,
  with a hint to make `git status` work there; `dubious ownership` is the common cause), and git
  missing from `PATH` is a warning notice (a picker warning in `InstallChoices`). This is what
  makes the ledger this checkout's own, which the install-by-source targets above and every
  removal rely on.
- **Canonical locations.** `source.Parse` resolves a folder or local archive with
  `filepath.EvalSymlinks(filepath.Clean(p))`, once (an archive link whose target has a different
  archive suffix is refused, since the suffix picks the extractor); `install` records a relative
  source against the project root with its links resolved (`canonicalRoot`), and every reader of a
  recorded relative source (`sourceRoot`: sync's validation, render's checks, `recordedSpec`)
  resolves it against that root too, so it resolves however the root was reached. The cache's
  same-source checks accept a location an earlier lola recorded through a link
  (`source.SameLocation`).
- **Hints.** Every `lola sync` command lola prints, in a hint, a notice, a dry run's closing lines
  or the picker's equivalent command, comes from `Engine.SyncCommand` (built on `Command`, which
  every other scoped command in a hint uses too): `lola sync`, then the scope's flag
  (`RootArgs`): `--user` at user scope, `--project` when `FindProject` of the working directory
  (`Engine.Getwd`, `os.Getwd` unless a spec sets it) is the scope's root (`FoundHere`, which
  install's equivalent commands use to decide `-C`), else `-C <root>`; a command without a scope
  flag would ask for its scope on a terminal (Plan 6b §12.3). Then the names and flags, each through
  `untrusted.Text` and `errs.ShellJoin`. A hint that repeats the user's run carries its names,
  `--update` and `--force`, and the hook flags only when given (`SyncRequest.rerun`); the
  "… and N more" of a capped file list or dry-run plan names the user's run with `-n --json`
  (`Engine.ListAll`). An install's `retry with: lola install <names>` (`withRetry`) is built per
  scope with `Command`, and left off a refusal for want of trust (`ErrNotTrusted`, the untrusted
  top-level targets), whose own hints are the way forward. `lola migrate` needs no case of its
  own: its process's directory decides, not the `Cwd` it passes.

## Layers

What a scope wants is a stack of **layers** (`internal/engine/profile.go`, Plan 6b §6.2), lowest
first: at user scope `default` (when `profiles/default.yml` exists), the active profiles in
`lock.json`'s order, then `user.yml`; at project scope the active profiles, then `project.yml`
(`user.yml` is never a project layer, D6). `Engine.layers` resolves the active list against
`profile.Visible` and reads each profile once per run (§19 S6); a stored name with no file is a
warning naming `lola profile <scope flag> <name> --off` (built by `Engine.Command`, e.g.
`lola profile --user user:go --off`) and counts as off, and a profile that does not load
refuses the run. A profile's relative local sources are made absolute against its directory
(§19 R5) before anything else sees them, so `lock.json` records them absolute. `desired(layers)`
walks the layers in order; a later entry replaces an earlier one of the same name whole and,
when they differ, adds a notice. Each `Wanted` carries its entry, `From` (the layer's name:
`project.yml`, `user.yml`, `profile:user:go`, `profile:default`) and `Over` (the layers it
replaced).

lola never writes a profile (D7, §19 R1): `pinsOf`, `Pins` and `needsPin` cover manifest
entries only. `validateSync` gives a profile's unpinned git or archive entry the commit and
digest `lock.json` records for the same source, ref and path, whichever layer won before
(`syncEntry.ledgerPin`; `from` is not compared, since a planted `lock.json` sets it too), so it installs as a pinned entry does until `--update` resolves its ref
again; the version installed is what `planSync` records anyway. At project scope such a pin is
checked like an untrusted entry's: its commit must be reachable from the ref even on a cache
hit, since an untracked `lock.json` planted in the checkout could name any commit (§11, S5). `--update` naming a profile's pinned entry is refused with
`edit the pin in <path>`; an unlimited `--update` leaves such entries at their pins with an info
notice. A pinned fetch's error names where the pin lives (`pinnedIn`).

`Status` (`lola list`) reads the same layers: a row's `From` is the layer whose entry wins now,
else the one `lock.json` recorded (the manifest's base name when it recorded none); a profile's
entry that `lock.json` does not record is `not_installed` like a manifest entry. It returns the
layers' notices (missing profiles) and refuses a project whose `lock.json` git tracks
(`refuseTrackedLedger`, Plan 6b §19 S5), since a committed one could turn profiles on.

`Render` reads the layers too (Plan 6b §7): `renderCheck` judges each installed module by its
winning entry, so a profile's module is not "not in manifest" at project scope, and a module no
layer lists is left out naming the layer `lock.json` recorded (`profile:user:go no longer lists
it`). A profile's entry skips the trust and folder-content checks, as at user scope, except
that one without targets of its own is left out when `project.yml`'s top-level targets, which it
installs to, are not the ones the trust record covers, and one rendering `lock.json`'s pin of a
git source is left out unless that commit is reachable from its ref (`source.CheckReachable`,
as sync checks a `ledgerPin`), since an untracked `lock.json` planted in the checkout could name
any commit. `renderPin`
installs the winning entry's pin, else `lock.json`'s version, and names where a failing pin
lives (an edit of the profile, or `sync --update` for the manifest). Render keeps or sets each
module's `from`, warns about missing profiles, and removes nothing for them.

`Profiles` and `ProfileState` (`internal/engine/profileview.go`) are what `lola profile` reads,
per scope. Both refuse a project whose `lock.json` git tracks before reading anything else
(§19 S5). `Profiles` reads the profiles directory's listing and `lock.json`, never a profile file
(§19 S8): each visible profile with its kind, path and 1-based position in the active list
(`default` is always active and has none), a missing-profile warning per stored name with no
file, and the invalid-name count as a warning. `ProfileState` resolves a typed name
(`resolveProfile`, §4.4 for the user kinds: a bare name is the user's profile, `default` is
`default.yml` at user scope only, `user:go` names its kind; `project:` and `remote:` names are
unknown until 6b.3, §19 P8; an unknown name lists the profiles the scope can use), then builds
the scope's layers with the profile appended to the active list when it is off, as `--on`
would, and reports each of its modules' winning layer (`From`) and the layers that entry replaced
(`Overrides`, from `Wanted.Over`). The profile is read once, by `layers`. Over several scopes,
one the name does not resolve in is left out; when it resolves in none, the first refusal is
returned. `lola profile` (`internal/cli/cmd_profile.go`) resolves its scopes with
`ResolveReadScope`, as `list` does, and prints a table whose rows name their scope;
`ProfileNames` completes its argument from the profiles directory's listing alone. Its action flags' aliases (`--enable`, `--use`, …) are one flag each: `aliasFlags`
(`internal/cli/help.go`) records them in the flag's `lola_aliases` annotation and installs a
pflag normalize function mapping each alias to its canonical name, and the usage template renders
`.LocalFlags` through `aliasedFlagUsages` (`--on (--enable, --activate)`). With an
action flag, `profileAction` checks the one-action rule on canonical flag names after cobra has
parsed (§19 R7), and `profileApply` resolves one scope as every writing command does (the picker
on a terminal; `both` refused), then runs `syncScope` with the `ProfileChange`: the same trust
and removal questions as `lola sync`, `-y` and `--trust` as answers. The summary
(`printProfileAction`: the list's new state, each change, then `printSync`'s lines) goes through
`pager.show`.

`Uninstall` refuses a module any profile layer lists (`refuseProfileModule`), even when the
manifest lists it too, since uninstalling would leave it wanted: each profile's hint is
`turn <name> off: lola profile <scope flag> <name> --off`, and `default`'s is the edit of
`default.yml`. `InstallCoaching` counts the not-installed entries of every layer and names the
layers they come from;
`InstallAll` reads each scope's layers under its lock, before anything is fetched (a profile
that does not load refuses the install), and adds the precedence notice of each entry it writes
that differs from the highest profile's (`overridesProfiles`).

## Sync

`Engine.Sync` (`internal/engine/sync.go`) makes one scope match its manifest. The CLI adapts it:
it answers the two questions (`SyncRequest.Trust`, `SyncRequest.ConfirmRemovals`) through the
Prompter on a terminal (`Prompter.Trust`, the picker, and `Confirm`), with `--trust` (which still
prints the diff it accepted) answering All and `-y` confirming, and nil (cannot ask) otherwise.
`SyncRequest.Modules` limits the run to the modules named (`syncLimit`); `Update` re-pins the run's
git and archive entries. The steps, in order, under the scope lock (none in a dry run):

0. Refuse a project at the home directory and symlinked project state; then a tracked
   `lock.json` (`refuseTrackedLedger`), before the profiles it names are read (§19 S5).
1. Read the layers; refuse a scope without a manifest when no profile is in play: nothing a layer
   wants, no active profile, no module `lock.json` says a profile installed (an empty manifest
   means "remove everything"; otherwise the scope syncs as an empty manifest, §19 R4, §10);
   refuse a name that is neither wanted nor an installed module (`limitTo`); sweep day-old
   `run-*` directories (best effort, also in a dry run) and create this run's.
2. Validate every entry of the run, `desired(layers)` in name order limited to the names, before any
   network contact or prompt: `source.Parse` against `sourceRoot` and `source.Validate` with the
   entry's ref and path (git's `check-ref-format`); the pin's shape against the source's kind; the
   cache holding the name from another source or path (`refuseOtherSource`, through
   `Store.Origin` and `source.SameLocation`), coached with `lola cache rm`, except a local source
   the cache records that no longer exists (`Origin.LocalGone`), which the run's publish replaces
   (`syncEntry.goneFrom`, a warn notice); the effective targets. An entry a profile won keeps that
   profile in `syncEntry.from` (written to `lock.json`'s `from`) and its file, which errors name.
   A limited run notes the modules it left out of sync (`outOfSync`, from the layers and
   `lock.json` alone).
3. At project scope, `trust.Check` of the record against the manifest's view gives the status and
   the changes, limited to the run (`runReport`: the named entries' changes, and the top-level
   targets only when a named entry installs to them). Coverage (`Covers`) is the entry's own; a
   profile's entry is the user's own and always covered.
4. Containment (never for a profile's entry, which is the user's, §19 R5): an uncovered folder or
   local archive, and a covered one whose source as written
   names a path inside the project (`lexicallyInside`: a pulled link can lead it out), must
   resolve inside the project's real root (`source.IsWithin`), else the run is refused before any
   diff, whatever `--trust` or `-y` say. Each covered manifest folder is then read (no network): content
   that is not the version `lock.json` records, or any content when it records none, marks the
   entry `folderChanged`, a trust change (`folderChanges`; an addition, `not installed yet`, when
   it records none: `notInstalled`). The removals are listed now
   (`syncRemovals`, from `lock.json` and the manifest; each path confined first, since `lock.json`
   is hostile input). Then the **early refusal**: a run that is not dry and needs an answer it
   cannot ask (the trust question with `Trust` nil, the removals with `ConfirmRemovals` nil)
   returns one `TrustRefusalError` naming every missing answer and cause (`syncRefusal`), with the
   field-only report (each entry to pin named with `pinReasons`, changed folders with their
   content) and the removals, before any fetch or `ls-remote`. Under `--json` the error document
   carries both (`JSONFields`, which `writeError` merges).
5. Resolve each entry: a folder is fetched every run; `--update` fetches the ref; an unpinned git
   or archive entry takes a snapshot of the cache head for its ref, or fetches the ref when there
   is none or the head has no commit (written before pins); a pinned entry takes a snapshot of
   its version (`Store.Pinned`), checking reachability for an uncovered git entry even on a hit,
   or fetches it by its commit (`fetchPin`). Every fetch is prepared in the run's staging
   (`Store.Prepare`) and must declare the entry's name. A covered entry at an unchanged pin (and
   not a changed folder) is committed to the cache now, without moving a head in a project dry
   run, and without replacing a gone source in one; everything else stays staged.
6. The report compares the record with the manifest **as it will be saved** (the pins the run
   writes applied), so a pin change is a trust change. `fillContent` (`content.go`) adds per added
   or changed entry the files against the version `lock.json` records (from the cache, never
   fetched; a lost or unreadable version lists every file as added with a note; the generated
   `lola.yml` only when it changed or went), the module MCP servers (`contentMCPServer`: argv and
   cwd quoted word by word as consent shows it, env, headers: the diff is their only consent),
   install hooks, and every runtime hook and agent MCP server the content declares
   (`declaredRuntimeHooks`: the module planned for every target lola knows without selections,
   probing no assistant), each MCP server and hook **marked** `added`, `changed` (a server's
   `previous` form attached), `unchanged` or `removed` against the version installed
   (`markServers`, `markHooks`; a hook is identified by all its fields). `note`, `reason`,
   `host_changed`, the folder lines (`folderEdits`) and `InstallsToTargets` complete it. Every value
   goes through `untrusted.Text`.
7. Plan every module (`planSync`) from the snapshot or staged tree, then ask. Dry: the report goes
   in the result. Otherwise `Trust` answers All or the entries accepted (`TrustAnswer`); one that
   accepts nothing is `ErrDeclined`. `accept` turns the answer into an `acceptance`: the accepted
   entries, whether the top-level targets are accepted (All, or an accepted added or changed entry
   without targets of its own; never a removed one), and the modules that **leave the run** (every
   change not accepted, and, when changed top-level targets are not accepted, every entry that
   installs to them). A partial answer drops those entries, their removals and their report
   changes, adds the `left as installed until you trust them` notice, and plans the rest again
   from the same staged trees under the same lock (`planSync` again, no fetch), so the summary is
   the apply. A declined entry's hooks never reach `prepare`.
8. Publish (`Store.Commit`, which checks the same-name rule again under the cache lock, and replaces
   a gone local source only after checking again that it is still missing): pinned fetches with
   `headIfAbsent`, the rest moving their ref's head; a declined entry's content is never
   committed. A dry run publishes nothing more, except `--update -n` at user scope.
9. Removals are confirmed through `ConfirmRemovals` before any hook consent is asked or recorded.
   A No (`declineRemovals`) applies nothing but saves `trust.Accept` of the loaded view for the
   answer given in step 7 (without the pins, which were not written), so the next sync asks
   again only the removals and the accepted entries whose pin or folder content it did not
   install; it returns a `RemovalsDeclinedError` (wrapping `ErrDeclined`) that says whether the
   record changed, names those entries (`Again`), says whether the record changes what
   `render` installs (`rendersOther`), and carries the run to repeat (`Retry`, `commandOf`: the
   sync, or an activation's `lola profile`).
10. `prepare` (runtime hook consent, the plan and its conflicts, install hook consent, the pending
    hook marks); a forced copy whose pending hook was declined again is kept by `keepUnhooked`
    (a dry run keeps it replaced, so its plan shows the hook); a dry run sets its closing commands
    (`ToApply`, `OffTerminal`, `EquivalentCommands`) and returns. `commit` applies the plan with
    the manifest edit of new pins staged in the same `plan.Apply` transaction (`saveManifest`). The trust record is written once, after the apply, and only
    when the user answered: `trust.Accept` of the loaded view for the accepted entries, through
    `saveManifest` when pins are written, else by `Trust.Save`, also when nothing was applied. A
    failed write fails closed: the error says what was applied and hints to run `lola sync` again
    (`syncAgain`, with the scope's flags; `install`, `uninstall` and `migrate` use it too).
11. `in_sync` is set when there is no step, pin, removal or module copy to replace and no
    active list an activation changes (one that moves no module still writes it); `Changed`
    names the modules the run changed. After a trust answer, `Equivalent` is the command that
    gives it off a terminal: the user's run with `--trust` for Trust all; for a partial answer,
    `lola sync --trust <names>` naming every accepted entry (a field-only change renders nothing
    but changes the record) and every changed module; `-y` when removals were confirmed. The
    CLI prints it after the removals are answered, when it asked the question.

**Activation** (`internal/engine/activate.go`, Plan 6b §6.3, §19 R3). `lola profile --on`,
`--off` and `--switch` are syncs: `SyncRequest.Profiles` (`ProfileChange{Action, Names}`) runs
under the same scope lock, after the tracked-`lock.json` refusal. `startActivation` lists the
profiles directory once, and `activeList` resolves each typed name (`resolveProfile`; only when
that finds none, `--off` also takes a stored name whose file is gone, as typed or as
`user:<name>`), refuses a name given twice, refuses `--off`/`--switch` of `default` (hint: rename
or delete `default.yml`) and any action on it at project scope, makes `--on default` a notice,
and builds the new list: `--on` appends in typed order (`by: on`), `--off` removes, `--switch`
replaces it in typed order (`by: switch`), at most 64. A list whose names do not change returns
at once with its notice (`already on (2nd of 3)`, `not on`, `already …`) and `in_sync`.
Otherwise the request's names become the qualified ones, so every hint repeating the run names
them as resolved. `loadProfiles` reads `default` and the union of the old and new lists once each,
keyed by qualified name, reusing that listing (a gone name warns only when the new list holds it);
`assemble` builds the old and the new layers from it. `layerChanges` diffs the new `desired`
against `lock.json` (`load`, `replace`, `unload`), and `moved` keeps what the change of list moves:
a module whose winning entry differs from the old layers' (absent, another layer, another entry),
and an installed module the new layers do not want that the old ones did or that `lock.json` says
a named profile installed. The other differences are pending from before: they are left to
`lola sync`, with the notice `N other changes are pending → lola sync <scope flag>`
(`outOfSync` is skipped). The run is limited to the moved modules (`syncLimit.only`, so none means
none), so trust (§8) and removals (§19 R15) see only them; precedence notices name only modules
the named profiles list (`desiredNoting`); the missing-manifest refusal does not apply. A trust
answer that leaves some entries as installed is refused: the new list is committed with the
apply, so an activation applies all it planned or nothing. `prepare`'s ledger gets the new list,
so it is written in the apply's commit, also when no module moves; a refusal, a No, an error or
an interrupt leaves the old list and files. The result embeds `*Activation`: `profiles` (the
list after the run), `changes` (`{module, action, from, over}`, `over` from `Wanted.Over`; an
unload's `from` is the layer `lock.json` recorded) and `Named`/`Files` (human output only). Hints
that repeat the run (`commandOf`) name `lola profile <names> --<action>` with the scope's flags
instead of `lola sync`.

**Prepare and commit.** `Store.Prepare` detects, normalizes, checks the cached copy reads the
same, and digests, in the run's staging, taking no lock and touching nothing in `modules/`.
`Store.Commit` copies the staged tree into a version (renamed into place, as before) under the
cache lock and sets the head; the run keeps rendering from its staged tree, the bytes that were
hashed. `Put`, used by install and `cache add`, is the two in a row, and lets a source the user
names replace a gone local one (`PutRequest.ReplaceGoneOrigin`). Content of an entry the user has
not trusted never enters the cache before they accept it, in a dry run included.

**Render.** `Render` checks every ledger module locally before anything is fetched
(`renderCheck`): no longer listed, another source than installed (or a folder whose cached source
is not the ledger's, at every scope), not covered or installing to uncovered top-level targets,
and a folder whose content does not load or is not the version installed (read into the run's
staging, never cached). The first that applies leaves the module as installed with a warn finding (codes
`not-in-manifest`, `source-differs`, `not-trusted`, `content-changed`) whose remedies `--strict`
lists instead of its generic advice (`Result.remedies`, `strictError`). The others render at the
manifest's pin, else the ledger's version (`renderPin`); a failed fetch under the pin is coached
as sync coaches it (`syncPinError`, `lola sync --update <m>`).

**Pager.** When stdout and stderr are both terminals (`WithTerminals` stands in for them in
specs), with human output and no `--no-pager`, `sync` reads `pager` (default `true`) from the
user's `config.yaml` (never a project's) and, when it is on, shows the trust diff, the removals
and a dry run's result through `pager.show` (`internal/cli/pager.go`, Plan 6b §9): it renders
them uncapped (an empty `more` lifts the `filesShown` and `stepsShown` caps) in the color of the
terminal, and runs `$LOLA_PAGER`, else `$PAGER`, else `less` (`more` on Windows), split on
whitespace with no shell and found with `exec.LookPath`; a pager in the current directory
(`exec.ErrDot`) is refused. The text goes in on a pipe (a pager that quits without reading it is
fine: exec drops the EPIPE of a clean exit); the pager writes to stdout; `LESS=FRX` is added to
`Engine.Environ` when `LESS` is unset. While it runs, `signal.Notify(os.Interrupt)` keeps Ctrl+C
from killing lola (the pager handles it). `show` returns when the pager exits, so the question
comes after it. A missing, refused or failing pager is a warning, and the capped text is printed
as before. An empty `$LOLA_PAGER` cannot be told from an unset one through the injected
`getenv`, so it falls through to `$PAGER`; `cat` means no pager.

**Accessible mode.** huh's line prompts read stdin in cooked mode, where Ctrl+C is a SIGINT.
While an accessible-mode form waits, `eofReader.catch` holds `signal.Notify(os.Interrupt)`; the
reader reads stdin in a goroutine, and an interrupt ends its input, so the form
returns `ErrAborted` (exit 0, the command's abort message). A goroutine left blocked on stdin
ends with the process, since a cancel always ends the command. lola installs no process-wide
handler.

## Security model

- **Hooks run code, with consent.** Install hooks run as the user, unconfined (sandboxing is on
  the roadmap), only after consent tied to the exact scripts. A changed script asks again; a
  changed file that a script runs does not, which the user docs say. Runtime hooks reach
  Claude Code's settings only under a consent tied to the rendered groups and the module
  version, so everything they could run from the module copy is covered; they go to the per-user
  settings file, never the shared one, and only when git cannot commit it.
- **Sources are not trusted.** Symlinks in module content are refused, including a file swapped
  for a symlink while it is copied; names that become paths or JSON keys are validated;
  `CopyTree` reads through `os.Root`; cache names and version ids are validated before use.
- **Destinations.** lola follows symlinked directories in the project, because dotfile managers
  use them, and shows writes that leave the project. **Residual risk:** a repository you check
  out can contain symlinks that redirect lola's writes elsewhere in your home directory.
  Conflict detection prevents overwriting content lola did not write, and the plan shows every
  redirected destination; review `lola install --dry-run` in repositories you do not trust.
- **Project state.** lola refuses a `.lola/`, `.lola/modules/` or `.lola/plugin-data/` that is
  a symlink, so a repository cannot point the module-copy swap, uninstall cleanup or plugin
  data outside the project, and it replaces a module copy that is not a real directory, so a
  committed link cannot point `${PLUGIN_ROOT}` and install hooks elsewhere.
- **Fetched sources.** Residual risks: lola honors the user's environment and git config
  (credential helpers, `insteadOf`, proxies) by design; git follows an initial http redirect
  itself, possibly to plain http; and a server can serve different content for the same ref.
  `cache update` and content digests make a change visible but do not prevent it; a version
  fetched again for an install must match its recorded digest, and a git commit must be on its
  ref. Checkouts convert nothing and run no filter program a repository's attributes name.
- **Recorded sources.** `project.yml` and `lock.json` may be someone else's: their sources,
  refs, paths, commits and digests are checked when they are loaded, before any git run, for
  every command that reads them (`list` included). Names,
  paths, manifest metadata and runtime hook fields pass through `untrusted.Text` before lola
  prints them; other text a module contains (frontmatter keys quoted in finding texts, for
  example) is not yet sanitized.
- **Pulled manifests.** An entry of `project.yml` lola did not write (a pull, a hand edit) is
  never installed by name or rewritten: see [Trust record](#trust-record). `lola sync` shows every
  change with the content it brings and acts only on a yes or `--trust`, never `-y`; off a
  terminal it refuses before any network contact; uncovered local sources, and covered ones
  written inside the project, must stay inside it, checked before the diff; a covered folder
  whose content is not the version installed is a trust change; untrusted content stays in the
  run's staging until the yes, so a dry run or a No caches none of it. Residual risk: in an
  interactive or dry run the untrusted content is fetched (from a validated location) before the
  question, so the diff can show it. `render` still renders the versions `lock.json` records,
  which a pull cannot change because the ledger is private, and a tracked `lock.json` is refused
  by `sync`, `render`, `install` and `uninstall`. Step paths in `--json` output stay raw
  (JSON-escaped): `lola migrate` reads them as paths.
- **Unparseable files** are never rewritten, `selections.yaml` and `trust.json` included.
- **User files are untrusted.** `config.yaml` and `selections.yaml` are size-capped and strictly
  validated; their keys and values reach the terminal only through `untrusted.Text`.
- lola's own state files are `0600`; its XDG directories `0700`.

## Testing

Ginkgo/Gomega specs per package; every spec calls `testenv.Isolate()`, which also replaces
`PATH` with the spec's own `bin/` (where specs put fake assistants) followed by a per-spec
directory of symlinks to the few tools specs need (`sh`, `git`, …), so a developer's real assistant
is never found. CLI specs run the real command tree in-process with buffered, non-terminal streams.
Convention specs walk the live command tree, and the conformance suite walks the live target
registry. Specs named after Python bugs pin the fixes (`corrupt JSON config is never
overwritten`, …). Git specs serve real repositories over loopback HTTP through `git http-backend`
(`testenv.ServeGit`); archive specs build hostile archives with the standard library's writers.
No spec uses the network. `task test MODE=llm` prints only failures.

**The migrate container suite** (`task e2e:migrate`, `e2e/migrate/`) checks `lola migrate`
against the real Python lola. `scripts/e2e-migrate.sh` builds an image once (python:3.13-slim,
pinned by digest, plus the Python lola from upstream at the commit the design was checked
against, its packages and build backend installed from `e2e/migrate/requirements.txt` with
`--require-hashes`) by `run` and `commit`, because buildah cannot mount a build context where
overlay mounts nest; the image is named after the base digest, that commit and a hash of
`build-image.sh` and `requirements.txt`. The container's main process is a
loopback smart-HTTP git server, so both lolas fetch a real git source. The Venom suite has the
Python lola install fixtures, snapshots the tree, runs lola-go's `migrate` (mounted read-only),
and `check.py` verifies the design's §12.7 assertions, including the §6 oracle: every path the
Python lola wrote ends up lola's, in the trash, kept, reported, or byte-identical for an
unmappable record. It needs podman or docker,
venom and the network on first use, skips without a container runtime, and is not part of
`task check`; CI would run it as a separate job on a runner with podman (`task e2e:migrate
MODE=llm`), caching the image between runs.

## lola migrate

`internal/migrate` adapts the Python lola's state to the engine; the CLI only renders its
`Result`. `Load` reads `installed.yml` and each named module's `.lola/source.yml` as untrusted
YAML (1 MiB, known keys with unknown ones as warnings, single-component names, no control or
format characters, aliases followed under a budget of 100000 names, counting list items, mapping
keys and records, since PyYAML writes them) and marks a `project_path` the design refuses as a
problem rather than an error. `NewPlan` groups records into destinations (Plan 5c §14), decides
each record's outcome, maps its source to what `lola install` takes, finds modules already
migrated by source in the destination's manifest, lists the paths each assistant recorded and what
is never migrated, and resolves each install's source once, through `InstallChoices`, pinning it
to that cached version (`InstallRequest.Version`). It verifies rather than emulates the Python
lola's module-root rules: a record listing a component the resolved version lacks is unmappable,
as is one whose source lola-go cannot fetch (hint: rerun once it is reachable), so lola-go never
reads `$LOLA_HOME/modules/<m>` to guess where the Python lola found the module. A module's records
at one destination migrate all or none, since an install would replace the Python lola's module
copy under a record left behind. The CLI computes the plan once and hands it to both the preview
and the confirmed run, so the run installs what the preview showed.

`Migrate` runs in phases so that nothing is written on a refusal or a cancel: a dry-run
`InstallAll` per destination (with the recorded paths that pass the guards as replaceable;
`coachClash` rewrites its refusal, naming each guarded recorded path a conflict lies at with its
guard, and each clash between two modules); the MCP entries left in place, across all
destinations, and the module copies and Python home they reference (each recorded module copy on
its own); the decisions per recorded path (owned → kept; guarded → kept, unless `--force` writes
it; written by the plan → replaced; else moved, or kept with `--keep-python`), refusing an install
that would swap out a `.lola/modules/<m>` neither the ledger nor a migrated record accounts for,
or that git tracks (read with `--literal-pathspecs`) or is a symbolic link, unless `--force` backs
it up; a consent pass, an `InstallAll` whose `BeforeWrite` stops it, so every hook question is
answered before anything is written; then, per destination, the real `InstallAll` with
`BeforeWrite` deciding again under the scope lock and copying into the trash the replaced paths
and every file a `--force` step writes over (where it really is, past the symbolic links on its
way; a directory copied in the same pass that holds it is its backup), the moves (each checked
against the ledger after the install), and the leftover files of replaced directories (unlinked
when they match the backup, read through `os.Root`, keeping a link lola-go writes through; listed
as kept instead with `--keep-python`). The Python home moves last. The trash journals each entry
in `manifest.txt`, synced, before it copies or moves; it refuses to write through a symbolic link
inside it, and a move across file systems syncs the copy's directories before removing the
original.

Two engine additions serve it: `InstallRequest.Targets` adds targets to what a module's
manifest entry has, since modules at one destination were installed for different
assistants, keeping a module the manifest has at the version its ledger records (in
`InstallChoices` too, so the plan checks that version), and `InstallSpec.BeforeWrite` runs
after every refusal and prompt, before any write.
