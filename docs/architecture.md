# Architecture

<!-- docs-audit decisions (2026-10-28):
     - SPLIT_CANDIDATE declined: a maintainer explanation doc that contributors search and
       cross-link by anchor; one file serves that better than several. GitHub's outline gives
       the table of contents. The audit cannot suppress this code for explanation docs, so
       expect it as a standing info item. -->

For contributors. This page describes what is built: sources, targets, the plan and its apply,
profiles, marketplaces, trust and `lola migrate`.

## Pipeline

```
source ──fetch──▶ staging ──Put (detect, normalize, digest)──▶ cache (versions, heads per ref)
                                      │ module.Load (IR, re-derived each time; no format adapter)
                                      ▼
                     resolver (cached module minus selections.yaml exclusions)
                                      ▼
                     templates rendered and rechecked, per target (internal/templating)
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

The terms, each covered in its own section below:

- **staging**: the run's private `run-*` directory under `$XDG_CACHE_HOME/lola/.staging/`, where
  fetched content is checked before the cache takes it ([Fetching sources](#fetching-sources)).
- **IR**: the module as lola models it in memory, which `module.Load` derives from a cached
  version each time and never stores ([Cache](#cache)).
- **intents**: what a target asks to be written, such as `WriteFile` or `SetJSON`
  ([Plans and the ledger](#plans-and-the-ledger)).
- **findings**: what a target cannot express, with a severity ([Targets](#targets)).
- **ledger**: `lock.json`, the observed state, recording what lola wrote in a scope.
- **manifest**: the desired state, `project.yml` at project scope.

`internal/engine` runs this pipeline for one scope; `internal/cli` is thin cobra wiring over it.

**The cache is written only when a run applies.** A sync or update fetches into its staging, and
commits to the cache (`publishRun`) after the last point that could stop it: every refusal (trust,
removals, `--at`, a conflict, a hook or script consent) and every No (to the trust question, the
removals, the receipt, a consent) leaves the cache byte for byte as it was, heads and fetch times
included. A dry run (`-n`) is the one exception: it caches what you trust at its pin.

## Packages

Maintainer notes on Packages. It has its own page: [Packages](architecture/packages.md).

## Selections and the install flow

How selections work and how the install flow runs. It has its own page: [Selections and the install flow](architecture/install-flow.md).

## Plans and the ledger

Maintainer notes on Plans and the ledger. It has its own page: [Plans and the ledger](architecture/plans-and-the-ledger.md).

## Templates

Maintainer notes on Templates. It has its own page: [Templates](architecture/templates.md).

## Targets

- **A target is a YAML spec** (`internal/targetspec/specs`) that the generic target in
  `internal/target/spec` renders through `internal/target/mech` mechanisms, one per artifact
  kind, with `internal/target/policy` supplying lola's severities, guards and hints. It implements
  the contract in `internal/target`; `internal/targets` registers the built-in specs in a fixed
  order (no `init()` registration). `targets.Load(paths.Paths)` also reads the user's specs from
  `paths.Paths.Targets` (`*.yaml`; `.yml` refused) and returns the `Registry` (built-ins in
  their order, a user spec of a built-in's id in its place, then user-only ids sorted); the CLI
  calls it once before it builds the command tree and passes the `Registry` down. A load error is
  fatal for every command and never falls back to a built-in. The CLI prints `note: <id> uses
  your target spec …` once per replaced built-in (human output), and the engine adds the info
  finding `target/user-spec`. User-writing guide: [Target specs](guides/target-specs.md). A
  `depguard` rule in `.golangci.yml` lets these packages import only the contract (exactly),
  `module`, `paths`, `plan`, `targetspec`, `untrusted`, the standard library and their vetted
  third-party packages (`yaml`, `toml`, `semver`), never the engine, the CLI or lola's state; a
  second rule keeps `mech` from importing `spec` or `policy`.
- **Scope and root.** `Detect(scope, root)` gets the target's root for the scope, and
  `PlanContext.Root` carries it into `Plan`. OpenCode needs both: its user-scope root is its own
  config directory, whose existence marks it as set up, and it writes to `opencode.jsonc` when
  the root has one, else `opencode.json`. Targets may read the disk this way; they never write.
- **Destinations.**
  - `Destinations(scope, root)` declares, pure like `Plan`, where the target writes:
    directories whose direct entries are units of one kind (`DestDir`: kind, path, and the
    extension stripped from a file's name) and the JSON objects of MCP servers (`ServerMap`:
    file and pointer). `lola tidy` sweeps only these.
  - Rules are declared in lola's own namespace directory (`.claude/rules/lola`, `.gemini/lola`,
    `.opencode/lola`), never the user's `.claude/rules`; `.gemini` and `.opencode` themselves,
    which hold the assistants' settings, are never declared.
  - A conformance spec ties them to `Plan` both ways: every file and MCP intent lies in a
    declared destination of its kind (a direct entry, or for skills and rules one level further
    down), and every declared destination receives one; `OpAddJSONMember` intents are exempt.
- **Findings, not errors.**
  - A target returns what it loses as `target.Finding`s with a severity: info, warn, or skip
    (that artifact is left out for that target). `Plan`'s error is only for failures that are
    not about module content.
  - `target.Skipped` applies `--force` (forceable skips are rendered, marked `Forced`);
    `engine.StrictError` decides what `--strict` refuses.
  - Load warnings become findings about the module; the engine's own messages stay `Notice`s.
  - A finding the user can act on carries a `Hint`. `Admit` names the attribute-file entry, name
    pattern or version; each target names its own template branch or change; and the engine
    appends the run with `--force` (`Options.ForceRun`) to a forceable skip, kept apart in
    `UserHint`. A hint telling the module's author what to change sets `AuthorHint`, which only
    `lola check` prints.
  - Each finding and `Notice` has a `target.Class`, set where it is made:
    - `Admit` classes what a target loses `detail`, and a runtime hook or a guard key `safety`;
    - targets class their own findings;
    - the engine classes mention notices `detail`, with their `Module`.
    - One made without a class marshals as `safety`, so a new kind is shown in full.
    - `planModule` stamps each finding with `Check`, the `lola check` at the run's scope that names
      the module by `Options.checkArg`: install's `mentionArgs` word, sync's `syncCheckArg` (the
      cache name, or in a dry run of an uncached version `checkAddr`, the entry's source); a mention
      notice gets the same from its words.
    - `printNotices` prints safety in full and counts detail per module and check, so each scope has
      its line, adding `-t`; detail without a `Check` is printed in full; `-v` (`style.verbose`)
      prints all. The install review page goes through it too.
    - `lola check` reports each module's mention notices (`CheckedModule.Notices`).
  - The coaching spec of the conformance suite drives each target's live table and own
    findings, and fails on a hintless finding its `nothingToDo` list does not explain.
- **Capability tables.** Each target declares `Capabilities`: supported artifact kinds,
  frontmatter keys with value shapes and patterns (or always dropped), required keys, name
  patterns, and content rules. `target.Admit` checks one artifact against them, returns the
  document without the dropped keys, and warns about YAML values that 1.1 and 1.2 parsers read
  differently (`module.Doc.Ambiguous`). Renderers render through `Admit`, so the table is both
  what lola checks and what it does. Value-dependent checks stay in the target's code and
  return the same findings.
- **Probes.** `Detect` combines a binary lookup over the absolute `PATH` entries
  (`paths.Paths.Search`; relative entries are ignored, so a repository cannot plant a binary)
  with the target's project markers, and executes nothing. Install, sync and check run each
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
- **The load path is format-free.**
  - `module.Load` is detection with no adapters, and it is how the cache's versions are read:
    the generated `lola.yml` wins precedence, and its `components` make discovery irrelevant, so
    the plugin manifests left in the cache are inert and a lola upgrade cannot reinterpret a
    cached version.
  - A spec loads each format's cached copy this way and checks that every target renders it
    exactly like the fetched one, and that normalizing it again changes nothing.
  - A cached load does not see what the adapter warned about, so `Store.Put` returns the version
    with detection's warnings.
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

At project scope the engine refuses to run when `.lola/`, `.lola/modules/` or `.lola/plugin-data/`
is a symlink (`refuseSymlinkedState`, before the lock is taken, also for dry runs and `status`): a
repository can commit one, and the copy swap would then rename and delete directories outside the
project, or plugin data directories would be created there. The same check refuses a symlinked
`.lola/project.yml`, which the manifest write would otherwise replace outside the project.
User-scope copies and your profiles (`default.yml` included) may live behind a symlink;
`manifest.Edit` then writes at the link's target.

Install hooks build inside copies (`npm install`, virtual environments), so a copy's content is
never compared with the cache. `sync` (and so `update`) replaces a copy only when its module's
version changes (the ledger digest differs, or there is no ledger entry), the copy is missing
(`needsCopy`), or the module gains a target the ledger does not record it on (`addsTargets`), whose
hooks never ran; an entry that is not a real directory, such as a committed symlink, counts as
missing.

`install` plans every copy as replaced from the hashed version, so its hooks run only in bytes the
consent was given for; a copy that holds that version already, for an install that adds no target to
those the ledger records, is planned forced (below) and kept when no hook runs.

A target the module is not installed on has never had its hooks run, so an install, sync or update
that adds one replaces the copy and declines, or refuses, hooks without consent. Each replaced copy
is assembled as `.<module>.new`, the current one moves to `.<module>.old`, and the new one takes its
place.

If the ledger is never committed (the `after` callback of `plan.Apply` runs only after the commit),
the old copies are restored; otherwise they are dropped. Copies of modules that leave the ledger are
removed. `status` reports a missing copy as `missing`, and `sync` restores it and reruns its hooks;
edits inside a copy are not reported.

## Install hooks

Maintainer notes on Install hooks. It has its own page: [Install hooks](architecture/install-hooks.md).

## Runtime hooks

The `claude-code` target renders each matcher group of a module's runtime hooks as one
`AddJSONMember` intent (kind `target.KindRuntimeHook`) under `/hooks/<Event>` of
`.claude/settings.local.json`, or `~/.claude/settings.json` at user scope. Command handlers get `CLAUDE_PLUGIN_ROOT` and
`CLAUDE_PLUGIN_DATA` (`PlanContext.PluginRoot` and `PluginData`, the same values `target.Expand`
uses): textual forms are replaced in one pass, shell form gains an `export` prefix, and exec form
becomes `env -- NAME=value NAME=value <command> <args>`. `--` must come before the assignments: GNU
`env` reads a `--` after them as the command.

Paths that are not shell-safe, commands `env` would misread, PowerShell handlers and a settings file
git could commit are forceable skips of the whole set; forced renders export single-quoted values
and refer to the variables, so a POSIX shell never parses a lola path as shell syntax (an unquoted
reference is still split at spaces and glob-expanded, so the docs tell authors to quote it).

A path holding `' " $` `` ` `` or `\` is never forced, since the forced form relies on those
characters' quoting rules. An event that is not an identifier is an unforceable skip. On Windows
only exec form fits: every `C:\` path holds `\`, so shell-form hooks are never forced there, and the
skip's hint names exec form alone.

**Git-ignore check.** Targets stay pure, so the engine computes
`PlanContext.SettingsExposed` once per settings file and command (`probeRun.settings`), for the
settings file the target writes, at project scope
and only for modules with runtime hooks: if the project or a parent holds `.git`, it runs
`git ls-files --error-unmatch` (status 1 means untracked) and `git check-ignore -q --no-index`
(status 0 means ignored) through `source.GitCommand`, whose `-c` overrides keep a cloned
repository's config from running programs (fsmonitor, hooks). Any other status, or no git, sets
the reason, and the target skips the hooks (fail closed).

**Frontmatter hooks.** Claude Code also runs a `hooks:` key in skill, command and agent frontmatter.
`module` checks every key equal to `hooks` ignoring case like `runtime_hooks`; the `claude-code`
target (`componentHooks`) gives their command handlers the same rewrite and renders the rest of the
document through `target.Expand` separately, so `Expand`'s raw substitution never reaches hook shell
text.

Loading refuses the two ways around this: hooks in a `.md` file under a command's sidecar
directory (Claude Code reads it as a command; lola copies it as a file), and a first line starting
with `---` that `ParseDoc` does not read as frontmatter (more dashes, trailing whitespace or text,
no line break), where another parser might find hooks. `allowed-tools` is installed as written, with
the warning `claude-code/allowed-tools`.

**Consent.** Invariant: every intent of kind `target.KindRuntimeHook`, every intent of a component
carrying hooks, and every intent of an agent with inline MCP servers (Claude Code starts them with
the agent) passes the consent gate. Such intents carry a `plan.HookConsent` naming the settings file
whose consent covers them (and, for a component, its kind, name, hooks and, for an agent, its inline
servers as rendered); an intent without one is refused if it has the kind `KindRuntimeHook` or,
whatever its kind, edits `/hooks` in a `.claude/settings.json` or `settings.local.json`.

In `execute`, before `--strict` and before `plan.Compute`, the engine groups a module's gated
intents by settings file and computes their digest: sha256 over each group's target, pointer and
canonical JSON, and each component's target, kind, name, canonical hooks and canonical inline
servers (all as rendered, so absolute paths are in it), then the content digest of the module's
version.

Consent is recorded in `consent.json` under `<module>/runtime-hooks/<sha256 of the settings file's
absolute path, symlinks resolved>`, which cannot collide with an install hook entry because module
names have no `/`, and gives each project and scope its own consent. A recorded digest, a
`--trust=runtime-hooks` or a yes from the CLI's prompt (each recorded after the run's receipt is
answered yes, before the first write) keeps the intents; a dry run makes the same decision without
asking or recording; otherwise the intents are dropped with `<target>/runtime-hooks-declined`, per
settings hooks and per component.

Dropping them is what makes consent fail closed: the planner sees the module's installed groups and
components as orphans and removes them. The consent view (`engine.RuntimeHook`) reads handler fields
by exact name, as Claude Code does, shows every handler or matcher-group field it does not describe
as JSON, and passes every string through `untrusted.Text`. An inline server's view entry has
`mcp_server` set and no event; it shows env and header values, since they change what runs.

**Drift.** A hook group is identified by its content, so an owned group missing from its settings
file is one the user edited or removed.

`plan.Compute` keeps a missing owned non-string member owned but absent (step `absent`) instead of
writing it again, which would run both copies; the engine reports `<target>/runtime-hook-drift` as
info with a hint (it records the user's choice, so `--strict` ignores it, and human output prints
info findings that carry a hint), and `--force` writes it again. On uninstall the member is
forgotten and the drift is info. String members and files keep the "was missing; rewritten" rule.

## Applying

`plan.Apply` runs under the scope lock: `$XDG_STATE_HOME/lola/locks/<hash>.lock` for a project,
named after the resolved `.lola/` path, or `$XDG_STATE_HOME/lola/.lock` at user scope. The lock
lives outside `.lola/`, so removing an empty `.lola/` never unlinks a lock another process is
waiting on.

Apply stages every new file next to its destination, moves each existing destination aside, renames
the staged files in, writes the ledger, and then, when the command changes them, each manifest in
turn (`project.yml`, then the project's profiles that a sync writes pins into): staged
next to it (at a symlink's target), the current one backed up with a hard link (a copy where links
fail) and left in place, the new one renamed over it, and the directory synced, so after a crash the
scope always has a manifest.

The ledger and manifest writes are part of the transaction: any failure up to and including them
restores files, ledger and manifests in reverse order. A `ManifestWrite` may carry a `Check`, run
just before it is staged: a profile's (`Profile.Unchanged`) refuses the apply when `.lola/` or
`.lola/profiles/` stopped being the project's own directories, or the file no longer holds the bytes
the run read.

Afterwards backups are removed, a logical symlink whose target was deleted is removed, and then each
of the plan's `Dirs` that is still an empty directory (`rmdir`; never a symbolic link); the `after`
callback then runs.

`plan.Compute` lists those `Dirs`, each with a `delete` step of kind `directory`: every logical
ancestor of a deleted file, below the directories directly under its target's root, that no file the
plan writes lies in, that is a real directory inside the resolved root, and that holds only deleted
files and directories listed too.

A symbolic link is never one (`Lstat`), so neither it nor the directory it leads to goes, nor any
directory above it, which still holds it; a directory below it goes when it resolves inside the root
(`~/.claude` linked to `~/dotfiles/claude`: an emptied `~/dotfiles/claude/commands` goes), never one
a link leads out of the root to. The directories directly under the root (`.claude/`, `.gemini/`,
`.opencode/`) always stay: `Target.Detect` looks for them, and lola does not record which
directories it created, so it cannot tell one it made from one the user made to be detected.

The bytes of the manifest come from `manifest.Edit`, run under the scope lock before `Apply`: it
reads the file again (refusing one that does not load) and edits the yaml.v3 node tree, touching
only the entries and fields the command changes and appending new entries, so comments and order
survive.

It decodes and validates the bytes it produces as `manifest.Load` would, so nothing it could not
load again (a commit from a corrupt cache index, say) is written. The file is encoded again
(yaml.v3, two-space indent), so blank lines between entries are dropped and indentation is
normalized.

`manifest.EditDocument` is that node edit alone; `profile.Profile.Pin` uses it for a project
profile's pins, on the bytes the run read and checked (refusing a file that changed since), and
checks the result loads as a profile.

`manifest.Load` and `ledger.Load` check every recorded source, ref, path, commit and digest
(`source.Recorded`) and refuse text that would not print as it is (`untrusted.IsPlain`), before
anything reaches git; `manifest.Load` reads through `yamldoc.Read`, the reader `config.yaml` and
`selections.yaml` use (non-blocking open, regular files only, 1 MiB, one document, no anchors,
aliases or merge keys), and reads `version` before the strict decode.

`.lola/` itself is created when the scope lock is taken; its `.gitignore`, which lola owns, is
written when a scope is committed, so a refusal or cancel never touches a `.lola/` that
already existed (the Python lola's, for example). On
release `.lola/` is removed only if this command created it and nothing was recorded
(`project.yml` absent, and an empty or absent ledger), so a failed or empty first run leaves no trace.

## Fetching sources

Maintainer notes on Fetching sources. It has its own page: [Fetching sources](architecture/fetching-sources.md).

## Cache

Maintainer notes on Cache. It has its own page: [Cache](architecture/cache.md).

## Trust kinds

Maintainer notes on Trust kinds. It has its own page: [Trust kinds](architecture/trust-kinds.md).

## Trust record

Maintainer notes on Trust record. It has its own page: [Trust record](architecture/trust-record.md).

## Layers

The stack of layers a scope's configuration is built from. It has its own page: [Layers](architecture/layers.md).

## Sync

How `lola sync` works. It has its own page: [Sync](architecture/sync.md).

## Marketplaces

Maintainer notes on Marketplaces. It has its own page: [Marketplaces](architecture/marketplaces.md).

## Security model

Maintainer notes on Security model. It has its own page: [Security model](architecture/security-model.md).

## Testing

Ginkgo specs run in-process on every `task check`, isolated from your home and assistants by
`testenv.Isolate()`; the end-to-end suites under `e2e/` run the release-built binary in a
hardened container (`task e2e`), before each release. [testing.md](testing.md) describes both:
the conventions, the git and archive harnesses, the runner, container and helpers, how to add a
suite or a case, and what is untested by design.

## lola migrate

`internal/migrate` adapts the Python lola's state to the engine; the CLI only renders its `Result`.
`Load` reads `installed.yml` and each named module's `.lola/source.yml` as untrusted YAML (1 MiB,
known keys with unknown ones as warnings, single-component names, no control or format characters,
aliases followed under a budget of 100000 names, counting list items, mapping keys and records,
since PyYAML writes them) and marks a `project_path` the design refuses as a problem rather than an
error.

`NewPlan` groups records into destinations, decides each record's outcome, maps its
source to what `lola install` takes, finds modules already migrated by source in the destination's
manifest, lists the paths each assistant recorded and what is never migrated, and resolves each
install's source once, through `InstallChoices`, pinning it to that cached version
(`InstallRequest.Version`).

It verifies rather than emulates the Python lola's module-root rules: a record listing a component
the resolved version lacks is unmappable, as is one whose source lola-go (this lola, as distinct
from the Python lola) cannot fetch (hint: rerun once it is reachable), so lola-go never reads
`$LOLA_HOME/modules/<m>` to guess where the Python lola found the module.

A module's records at one destination migrate all or none, since an install would replace the Python
lola's module copy under a record left behind. The CLI computes the plan once and hands it to both
the preview and the confirmed run, so the run installs what the preview showed.

`Migrate` runs in phases so that nothing is written on a refusal or a cancel:

- A dry-run `InstallAll` per destination (with the recorded paths that pass the guards as replaceable; `coachClash` rewrites its refusal, naming each guarded recorded path a conflict lies at with its guard, and each clash between two modules)
- The MCP entries left in place, across all destinations, and the module copies and Python home they reference (each recorded module copy on its own)
- The decisions per recorded path (owned → kept; guarded → kept, unless `--force` writes it; written by the plan → replaced; else moved, or kept with `--keep-python`), refusing an install that would swap out a `.lola/modules/<m>` neither the ledger nor a migrated record accounts for, or that git tracks (read with `--literal-pathspecs`) or is a symbolic link, unless `--force` backs it up
- A consent pass, an `InstallAll` whose `BeforeWrite` stops it, so every hook question is answered before anything is written
- Then, per destination, the real `InstallAll` with `BeforeWrite` deciding again under the scope lock and copying into the trash:
  - the replaced paths and every file a `--force` step writes over (where it really is, past the symbolic links on its way; a directory copied in the same pass that holds it is its backup),
  - the moves (each checked against the ledger after the install), and
  - the leftover files of replaced directories (unlinked when they match the backup, read through `os.Root`, keeping a link lola-go writes through; listed as kept instead with `--keep-python`).

Then the Python lola's marketplaces are registered (below),
and the Python home moves last. The trash journals each entry
in `manifest.txt`, synced, before it copies or moves; it refuses to write through a symbolic link
inside it.

`trash.Move` takes the entry's parent as an `os.Root` and its name: the parent's path names the
entry in the manifest and for the rename. Across file systems it copies through that root (no link
followed, no path resolved again), syncs the copy's directories, then removes through the same root
only the files and links whose fingerprint matches the copy (`UnlinkIfBackedUp`'s rule) and the
directories then empty; what remains was created or changed after the copy and comes back as a
`*trash.ChangedError` with the trash path.

`CopyBytes` writes given bytes, never reading the original, as a new 0600 file.
Migrate reports such a partial move with the trash path of the part moved (the Python home's
`reason` says `moved in part`); that branch has no spec, as no fixture can time a change between the
copy and the removal ([testing.md](testing.md#untested-by-design)).

Marketplaces: `Load` also reads `market/<name>.yml` (`name`, `url`, `enabled`, with
the same guards; a file whose name would not print as itself is skipped with a warning; `cache/` is
never read).

A `url` is shown only through `shownURL` (`source.DisplayURL`: no user info, password, query or
fragment), and `translate` puts every source through the rule `--add` applies (`source.Validate` for
git, `source.CheckURL` for a catalog file), so one with credentials or a query is `not-registered`
with no hint; only `source.IsSCP` text is git's scp form, so `ext::…` or `a:/b` is refused, never a
folder under the working directory. Every name the plan may register is claimed before any slug is
chosen.

`planMarkets`, in `NewPlan`, decides each without any network contact, against your registrations
(`engine.Markets`): `translate` maps the `url` lexically, but for a `file://` URL or path, which it
stats to tell a folder from the catalog file in it (`--path`), on this machine and the user's own; a
`url` lola cannot read is `not-registered` with its reason and no hint; a name `market.ValidateName`
refuses, or one of yours has from another source, is `not-registered` with a `market.FreeName`
slug's `--add`; a disabled one with its `--add`; the same name, source and (when the url names one)
path is `already-registered`.

The run's `registerMarkets` calls `engine.AddMarket` for each planned one, after the destinations
and before the home's decision, with `PythonName` for a tree whose url names no file
(`Market.lookup`): `fetchMarket` then finds the catalog with `pythonCatalog` (the Python lola's
`_pick_marketplace_yaml`: `<name>.yml`/`.yaml`, `marketplace.yml`/`.yaml`, else the only root
`.yml`/`.yaml` but `.pre-commit-config.yaml`) instead of `catalogIn`, and the registration records
that file.

A failure is that marketplace's `failed` outcome (its hints, then its `--add`, or for a lookup url a
command that reruns `lola migrate`, since an `--add` would not read the Python lola's file; nothing
for `ErrNoPythonCatalog`), never the run's.

A skipped one whose hint needs that file is fetched for it in the run (`engine.PythonCatalog`); a
preview cannot tell, so it says the run gives the command (`marketReport`). A marketplace not
registered keeps the Python home, as an unmappable record does, unless it is only disabled; a
planned one makes the run something to do (`nothing`).

Two engine additions serve it: `InstallRequest.Targets` adds targets to what a module's
manifest entry has, since modules at one destination were installed for different
assistants, keeping a module the manifest has at the version its ledger records (in
`InstallChoices` too, so the plan checks that version), and `InstallSpec.BeforeWrite` runs
after every refusal and prompt, before any write.

## lola tidy

Maintainer notes on lola tidy. It has its own page: [lola tidy](architecture/lola-tidy.md).

## lola repair

`Engine.Ledger` is the only engine read of a scope's `lock.json`, apart from `market_sync`'s
tolerant read and `shouldRemoveLola`. It reads with `ledger.LoadKeepingStrays`, then refuses the
first `ledger.Stray`, judged against the engine's targets (`knows`), as a `*ledger.StrayError`
hinting `lola repair` for the scope. No request reaches `Ledger`, so the hint keeps the run's
output flags through `Engine.Display`, which the CLI sets once per run. `fsx.Resolve` still
refuses a non-local path in planning, as defense in depth.

`PlanRepair` lists the strays and the modules `Forget` would leave on no target, writing nothing
and taking no lock; the CLI asks about that plan, or prints it under `-n`. `ApplyRepair` takes
the scope lock, reads `lock.json` again, refuses a stray the plan did not list (a hand edit
while it waited), forgets the rest, and saves `lock.json` only when it forgets something. It
deletes no file and does not check whether git tracks `lock.json`, since it deletes nothing.
