# Architecture

For contributors. The design spec is `docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`;
this page describes what is built (Plans 1 to 3).

## Pipeline

```
source ──fetch──▶ staging ──Put (normalize, digest)──▶ cache (content-addressed versions, heads per ref)
                                      │ module.Load (IR, re-derived each time)
                                      ▼
                     targets (pure: IR → intents + findings)
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

| Package | Responsibility |
|---|---|
| `paths` | XDG resolution from an injected `getenv`; no globals. |
| `fsx` | Atomic writes, symlink-aware destination resolution, comment-preserving JSONC edits (object members and string array elements), mode normalization. |
| `module` | The IR, frontmatter parsing, name rules, loading `lola.yml` + the native layout, rules, hook validation, and normalization. |
| `source` | Source parsing and the folder fetcher. |
| `store` | The cache's storage: content-addressed versions, per-ref heads, the cache lock. (Internal name; user-facing text says "cache".) |
| `ledger`, `manifest` | Observed and desired state, deterministic IO. |
| `consent` | Recorded consent to run a module's install hooks, keyed by the hooks' digest. |
| `target` | The target contract: `Target`, `PlanContext`, `Finding`, capability tables and `Admit`, `Probe`, and helpers renderers share. No implementations. |
| `target/claude` | The `claude-code` renderer and its capability table. One package per target. |
| `target/conformance` | Specs every registered target must pass. |
| `targets` | The registry: the one list of every target. |
| `plan` | Intents, `Compute` (diff + conflicts) and `Apply` (transaction). |
| `engine` | Install, uninstall, render, status, fetch, update, search; module copies and hooks. |
| `cli` | Commands, help, aliases, completion, output and prompts. |
| `errs` | Errors that carry coaching hints. |
| `testenv` | Test isolation (`Isolate()`: fresh `HOME`, XDG dirs, git config, and a `PATH` without the developer's assistants). |

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
exactly this computation.

Hashes: files are hashed by bytes, JSON members by their canonical JSON (sorted keys, no
comments). JSON edits operate on the hujson AST so bytes outside the edited member are
preserved; trailing line comments belong to the line they end. `ledger.created` remembers the
JSON files lola created, so they are deleted when lola's last member leaves them empty.

**Owned array members.** `AddJSONMember` claims one string in a JSON array that users also edit
(rule references for Gemini and OpenCode, Plan 4). The artifact records the string as `value`,
which is part of its identity, so several modules can each own an entry in one array; the hash is
over the string's canonical JSON. A member is present or absent, never hand-edited: one the user
already listed is adopted, one the user removed is rewritten with a note, and an orphan the user
removed is forgotten. A `created` entry with a `key` records an array lola created; when lola's
last member leaves it, the array goes too, with the parent objects it leaves empty, and the file
if lola created that. A value at the pointer that is not an array is a hard error. Appending
keeps comments that follow the last member before the closing bracket or brace, so removing
lola's entry restores the file byte for byte.

**Rules** are plain files each target writes where its assistant loads them (Claude Code:
`.claude/rules/lola/<module>/<rule>.md`). Every artifact belongs to exactly one module; lola
edits no user-owned instruction file, so nothing is shared between modules.

## Targets

The design is `docs/superpowers/specs/2026-10-09-target-framework-design.md`.

- **One package per target** (`internal/target/claude`, …) implements the contract in
  `internal/target`; `internal/targets` lists them explicitly (no `init()` registration). A
  `depguard` rule in `.golangci.yml` lets target packages import only the contract, `module`,
  `paths`, `plan`, the standard library and their vetted third-party packages, never the engine,
  the CLI or lola's state.
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
  cached per binary build in `$XDG_CACHE_HOME/lola/probes.json`. The version only feeds
  `PlanContext.Version`, which `Admit` compares with capability `Bounds` to add findings;
  rendering always follows the newest version, so output is the same on every machine.
- **`lola check`** (`engine.Check`) stages a folder like `cache add` into a temporary directory
  (or reads a cached module), runs every target's `Plan` and keeps only the findings.

## Module copies

`${PLUGIN_ROOT}` expands to the module copy, `.lola/modules/<module>/` (user scope:
`$XDG_DATA_HOME/lola/user-modules/<module>/`), never into the cache. The path does not change
between versions, so rendered files stay valid while a copy is replaced. Targets read artifact
content from the cache version (`PlanContext.ContentDir`) and expand `${PLUGIN_ROOT}` to the
copy (`PlanContext.PluginRoot`), because the copy is only put in place after the plan is
computed.

At project scope the engine refuses to run when `.lola/` or `.lola/modules/` is a symlink
(`refuseSymlinkedState`, before the lock is taken, also for dry runs and `status`): a repository
can commit one, and the copy swap would then rename and delete directories outside the project.
User-scope copies may live behind a symlink.

Install hooks build inside copies (`npm install`, virtual environments), so a copy's content is
never compared with the cache. A copy is replaced only when its module's version changes (the
ledger digest differs, or there is no ledger entry) or the copy is missing (`needsCopy`). Each
replaced copy is assembled as `.<module>.new`, the current one moves to `.<module>.old`, and the new one takes its place. If
the ledger is never committed (the `after` callback of `plan.Apply` runs only after the commit),
the old copies are restored; otherwise they are dropped. Copies of modules that leave the
ledger are removed. `status` reports a missing copy as `missing`, and `render` restores it and
reruns its hooks; edits inside a copy are not reported.

## Install hooks

`pre-install` and `post-install` scripts must be executable regular files inside the module
(checked when the module is loaded). After the plan is computed and before anything is written,
the engine decides consent per module: `--allow-hooks` (this run only), a recorded consent for
the same digest (sha256 over each hook's event, script path and script sha256), or the CLI's
prompt, whose yes is recorded in `$XDG_STATE_HOME/lola/consent.json`. Without consent a
`pre-install` hook is an error and a `post-install` hook is declined with a warning.

Hooks run once per target, in the module copy, with the parent environment plus
`LOLA_MODULE_NAME`, `LOLA_MODULE_PATH`, `LOLA_PROJECT_PATH` (the target's scope root),
`LOLA_ASSISTANT`, `LOLA_SCOPE` and `LOLA_HOOK`, under a 10-minute timeout. A failing
`pre-install` hook restores the old copies and writes nothing; `post-install` hooks run after
the commit, so a failure is a warning. `install` runs hooks for every module it installs;
`render` only for modules whose copy it replaces (a new version, or a missing copy). A dry run reports hooks as `would-run`.

## Applying

`plan.Apply` runs under the scope lock: `$XDG_STATE_HOME/lola/locks/<hash>.lock` for a project,
named after the resolved `.lola/` path, or `$XDG_STATE_HOME/lola/.lock` at user scope. The lock
lives outside `.lola/`, so removing an empty `.lola/` never unlinks a lock another process is
waiting on. Apply stages every new file next to its destination, moves each existing
destination aside, renames the staged files in, and writes the ledger. The ledger write is part
of the transaction: any failure up to and including it restores everything in reverse order.
Afterwards backups are removed, emptied directories are pruned (never past a target's base
directories), and a logical symlink whose target was deleted is removed. The manifest is
written last, through the `after` callback; if it fails, the already-applied files stay and the
error is returned.

`.lola/` itself is created when the scope lock is taken, with a `.gitignore` lola owns. On
release `.lola/` is removed only if this command created it and nothing was recorded (no
`project.yml`, and an empty or absent ledger), so a failed or empty first run leaves no trace.

## Cache

```
$XDG_CACHE_HOME/lola/modules/<name>/
  index.yml                # source, type, path, heads: [{ref, digest}]
  versions/<digest12>/
    content/               # the normalized module: native layout + one generated lola.yml
    .lola-origin           # format, manifest, others_present, ref, commit, fetched_at, digest
```

`cache add` loads the staged module, normalizes it (`module.Normalize`: one generated
`lola.yml`, every other manifest removed), digests the result, and renames the version into
place in whole from `$XDG_CACHE_HOME/lola/.staging/` (same filesystem), under
`$XDG_CACHE_HOME/lola/.lock`. `.lola-origin` records the detected format and which manifests
were read and ignored. Module names and version ids read back from the cache are validated
before they are used in a path. The IR is re-derived from `content/`, never stored.

Installs run from their module copies, so the cache is re-fetchable: `render` looks up the head
for each installed module's ref and, if it is missing (for example after `lola cache rm`),
fetches again from the cache's recorded source or the source in the ledger, failing with a hint
only if neither is usable. `status` reports `stale` when the head's digest differs from the
ledger's.

## Security model

- **Hooks run code, with consent.** Install hooks run as the user, unconfined (sandboxing is on
  the roadmap), only after consent tied to the exact scripts. A changed script asks again; a
  changed file that a script runs does not, which the user docs say.
- **Sources are not trusted.** Symlinks in module content are refused, including a file swapped
  for a symlink while it is copied; names that become paths or JSON keys are validated;
  `CopyTree` reads through `os.Root`; cache names and version ids are validated before use.
- **Destinations.** lola follows symlinked directories in the project, because dotfile managers
  use them, and shows writes that leave the project. **Residual risk:** a repository you check
  out can contain symlinks that redirect lola's writes elsewhere in your home directory.
  Conflict detection prevents overwriting content lola did not write, and the plan shows every
  redirected destination; review `lola install --dry-run` in repositories you do not trust.
- **Project state.** lola refuses a `.lola/` or `.lola/modules/` that is a symlink, so a
  repository cannot point the module-copy swap and uninstall cleanup outside the project.
- **Unparseable files** are never rewritten.
- lola's own state files are `0600`; its XDG directories `0700`.

## Testing

Ginkgo/Gomega specs per package; every spec calls `testenv.Isolate()`, which also replaces
`PATH` with the spec's own `bin/` (where specs put fake assistants) followed by a per-spec
directory of symlinks to the few tools specs need (`sh`, `git`, …), so a developer's real assistant
is never found. CLI specs run the real command tree in-process with buffered, non-terminal streams.
Convention specs walk the live command tree, and the conformance suite walks the live target
registry. Specs named after Python bugs pin the fixes (`corrupt JSON config is never
overwritten`, …). `task test MODE=llm` prints only failures.
