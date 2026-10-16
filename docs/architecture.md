# Architecture

For contributors. The design spec is `docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`;
this page describes what is built in Plan 1. Where Plan 1 differs from the target spec, see the
transitional note in [decisions.md](decisions.md).

## Pipeline

```
source ──fetch──▶ staging ──Put──▶ cache (content-addressed versions, heads per ref)
                                      │ module.Load (IR, re-derived each time)
                                      ▼
                     targets (pure: IR → intents + notices)
                                      ▼
            plan.Compute (diff vs ledger + disk, conflict matrix) ──▶ Plan
                                      ▼
            plan.Apply (stage, back up, rename, write ledger, rollback on error)
                                      ▼
                          manifest saved (desired state)
```

`internal/engine` runs this pipeline for one scope; `internal/cli` is thin cobra wiring over it.

## Packages

| Package | Responsibility |
|---|---|
| `paths` | XDG resolution from an injected `getenv`; no globals. |
| `fsx` | Atomic writes, symlink-aware destination resolution, comment-preserving JSONC member edits, markdown pointer blocks, mode normalization. |
| `module` | The IR, frontmatter parsing, name rules, and loading `lola.yml` + the native layout. |
| `source` | Source parsing and the folder fetcher. |
| `store` | The cache's storage: content-addressed versions, per-ref heads, the cache lock. (Internal name; user-facing text says "cache".) |
| `ledger`, `manifest` | Observed and desired state, deterministic IO. |
| `target` | The `Target` interface and the `claude-code` renderer. |
| `plan` | Intents, `Compute` (diff + conflicts) and `Apply` (transaction). |
| `engine` | Install, uninstall, render, status, fetch, update, search. |
| `cli` | Commands, help, aliases, completion, output and prompts. |
| `errs` | Errors that carry coaching hints. |
| `testenv` | Test isolation (`Isolate()`: fresh `HOME`, XDG dirs, git config). |

## Plans and the ledger

Targets return **intents** (`WriteFile`, `CopyTree`, `SetJSON`, `PointerBlock`). `plan.Compute`
expands trees, then, in two passes:

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
comments), pointer blocks by their text. JSON edits operate on the hujson AST so bytes outside
the edited member are preserved; trailing line comments belong to the line they end.

**Shared artifacts** (the `CLAUDE.md` pointer block) are re-derived on every operation from the
set of modules that ship instructions, via `Target.Shared`. `ledger.created` remembers shared
files lola created, so they are deleted when lola's last content leaves them empty.

## Applying

`plan.Apply` runs under the scope lock (`.lola/.lock` or `$XDG_STATE_HOME/lola/.lock`):
stage every new file next to its destination, move each existing destination aside, rename the
staged files in, write the ledger. The ledger write is part of the transaction: any failure up
to and including it restores everything in reverse order. Afterwards backups are removed,
emptied directories are pruned (never past a target's base directories), and a logical symlink
whose target was deleted is removed. The manifest is written last, through the `after` callback;
if it fails, the already-applied files stay and the error is returned.

`.lola/` itself is created when the scope lock is taken. On release it is removed only if this
command created it and nothing was recorded (no `project.yml`, and an empty or absent ledger), so
a failed or empty first run leaves no trace.

## Cache

```
$XDG_DATA_HOME/lola/modules/<name>/
  origin.yml               # source, type, path, format, heads: [{ref, digest}]
  versions/<digest12>/
    content/               # copy of the module: regular files only, modes 0644/0755
    meta.yml               # ref, commit, fetched_at, full digest
```

Versions are immutable, so `${PLUGIN_ROOT}` paths never change under a running MCP server, and
heads per ref let projects pin different refs. Versions are assembled in
`$XDG_DATA_HOME/lola/.staging/` (same filesystem) and renamed in whole, under
`$XDG_DATA_HOME/lola/.lock`. The IR is re-derived from `content/`, never stored.

`render` looks up the head for each installed module's ref. If the head is missing (for example
after `lola cache rm`), it fetches again from the cache's recorded source, or from the source in
the ledger, and fails with a hint only if neither is usable. `status` reports `stale` when the
head's digest differs from the ledger's.

Planned (Plan 2): per-project module copies, normalization at add, and the cache under
`$XDG_CACHE_HOME`. This section will change with it.

## Security model

- **No code execution.** Module hooks are detected and listed, never run in this build.
- **Sources are not trusted.** Symlinks in module content are refused; names that become paths
  or JSON keys are validated; `CopyTree` reads through `os.Root`.
- **Destinations.** lola follows symlinked directories in the project, because dotfile managers
  use them, and shows writes that leave the project. **Residual risk:** a repository you check
  out can contain symlinks that redirect lola's writes elsewhere in your home directory.
  Conflict detection prevents overwriting content lola did not write, and the plan shows every
  redirected destination; review `lola install --dry-run` in repositories you do not trust.
- **Unparseable files** are never rewritten.
- lola's own state files are `0600`; its XDG directories `0700`.

## Testing

Ginkgo/Gomega specs per package; every spec calls `testenv.Isolate()`. CLI specs run the real
command tree in-process with buffered, non-terminal streams. Convention specs walk the live
command tree. Specs named after Python bugs pin the fixes (`corrupt JSON config is never
overwritten`, …). `task test MODE=llm` prints only failures.
