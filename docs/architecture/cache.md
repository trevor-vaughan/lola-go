# Cache

Maintainer notes on Cache. It is split out of [architecture](../architecture.md).

```
$XDG_CACHE_HOME/lola/modules/<name>/
  index.yml                # origins: [{source, type, path, components, heads: [{ref, digest, commit}]}],
                           #   newest first, one per source the module was fetched from;
                           #   last_used: {<digest12>: YYYY-MM-DD}, shared (versions are by content)
  versions/<digest12>/
    content/               # the normalized module: native layout + one generated lola.yml
    .lola-origin           # format, manifest, others_present, ref, commit, fetched_at,
                           #   digest, executables
```

The digest is sha256 over, for each regular file under `content/` in byte order of its slash
path, `path NUL octal(mode) NUL decimal(length) NUL content`, where mode is `0755` for a path in
the version's `executables` and `0644` otherwise (the generated `lola.yml` never is). A change to
its input, or to the bytes a fetch and `module.Normalize` produce for unchanged content, bumps
`manifest.Version`; a golden-digest spec guards it.

A head records the commit its fetch
resolved; `.lola-origin`'s `commit` is only the first fetch's, which may be another commit with
the same content. Putting content the cache already holds rewrites the version's `executables` when
they differ, so a version cached before execute bits were recorded is repaired by the next fetch
of it (`cache update`, or `install` of its source).

`cache add` detects the staged module's format and reads it (`module.Detect`), normalizes it
(`module.Normalize`: one generated `lola.yml`, every other lola manifest removed), digests the
result, and renames the version into place in whole from the run's staging directory under
`$XDG_CACHE_HOME/lola/.staging/` (same filesystem), under `$XDG_CACHE_HOME/lola/.lock`.
`.lola-origin` records the detected format and which manifests were read and ignored.

Module names and version ids read back from the cache are validated before they are used in a path;
a head's commit must be empty or a full SHA, and a version's recorded digest must be the one its id
abbreviates, or the cache coaches `lola cache rm`. The IR is re-derived from `content/`, never
stored.

Installs run from their module copies, so the cache is re-fetchable. `sync` renders installed
modules at the version the ledger records, never a head, so `cache update` changes no installation:
for a git or archive module `Store.Pinned` copies that version's `content/` into a **snapshot** in
the command's private `run-*` staging directory, hashes the copy with the recorded executables, and
loads the module from it, so the IR, the intents and the module copy all come from the bytes that
were hashed.

On a miss (after `lola cache rm`, say) `fetchPin` fetches it again from the cache's recorded source
or the ledger's: a git commit must be reachable from the ref and is fetched by SHA; the content is
prepared in the run's staging (`Store.Prepare`) and must declare the module's name
(`PutRequest.Name`) and have the recorded digest, or nothing is cached; then the run commits it
(`Store.Commit` with `headIfAbsent`, so the head for the ref is set only if it has none) and renders
from the staged tree it hashed.

A folder module is read from its folder (the version installed when the folder is gone), and
refused, with nothing cached, when it now declares another name. When the version cannot be obtained
(a git entry with no recorded commit, a fetch that serves another commit, other content than the
recorded digest) the error carries two hints: `lola install <source> [--ref r] [--path p]`
(`installCommand`), which installs what the source serves now, and `lola uninstall <name>`.

`install` pins git and archive entries (`commit` from the fetch or head, `digest`); folders are
never pinned. `status` reports `stale` when the manifest's pin differs from the ledger's digest, and
`update_available` when the cache head for the entry's ref differs from the pin (or from the
ledger's digest when unpinned).

`last_used` is the UTC day a run last used each version. `Store.Commit` writes it
for every version it commits, `Store.Pinned` for the version `sync` and `profile` read,
and `InstallAll` (`Store.Used`) for the version each request resolves, outside a dry run, so
`lola info`'s plan records none. `Store.Used` reads the index without the lock and, only when the
day differs, takes `.lock`, reads it again and writes it: at most one write per version per day.

The store's clock is the engine's `Now`, read at each use, so specs fake the day. Recording is best
effort: `Store.Pinned` and `InstallAll` go on when `Store.Used` cannot lock or write the index (a
read-only cache). An entry read back whose id or day is not one lola writes is dropped, never
refused: its key reaches no path, and its version counts as first seen by the next clean.

Each staging command's `run-*` directory under `.staging/`, and `lola check`'s `check-*` under
`tmp/`, is held by an exclusive flock on `<dir>.lock` beside it for the run's life (`scratch`
in `internal/engine/staging.go`): the lock file is created and locked before the directory exists,
and the run removes the directory, then releases and removes the lock. A lock file another lola
removed after this run created it and before it locked it is detected (`os.SameFile` of the locked
descriptor and the path) and another name is taken.

`sweepRuns` removes a `run-*` older than a minute
only when it can take its lock without waiting, so a run waiting at a prompt for longer keeps its
scratch, and removes its lock after releasing it. A `run-*.lock` without its directory (its
run killed after the directory was swept, or between creating the lock and the directory) is
removed under the same rule: older than a minute and free.

`lola cache clean` is `engine.PlanClean`, which writes nothing, and `engine.ApplyClean`, which
applies the plan the user confirmed (`internal/engine/clean.go`). The plan lists superseded versions
(no head points to them), with an age (`--unused-for`, else `config.yaml`'s `cache_unused_for`) the
versions unused that long, heads included, and the scratch whose lock it can take without waiting;
`--all` lists every entry of `modules/`, `markets/`, `.staging/` and `tmp/` but held scratch.

A module whose `index.yml` it cannot read, or an entry it cannot read, is kept as `unreadable` and
planning goes on. Apply takes the markets lock (for `--all`), then the cache lock (`Store.Lock`) for
its whole removal.

Under it, first, every item is checked again: every directory between the cache's root and it is
still a real directory (`realDirs`, `Lstat`), it is still there, a superseded version still
headless, an unused one still unused; one that changed is kept and reported, as is every item of a
module whose directory is not real (one check per module decides both its items and its index); a
module whose index it cannot read again, or one of whose versions it cannot examine, is kept as
`unreadable` with all its items and its index unwritten, and the clean goes on; `.staging`, the
rename target, must be a real directory too.

Then, before any rename, each module's index drops the heads and `last_used` of its versions that go
and dates every version without one (`Store.Cleaned`), so a kill leaves no head naming a removed
version; an index write that fails, or a rename, puts back, from the index read before, the heads
and `last_used` of the versions that may still exist (`Store.Restore`), and a failed rename's error
is a `CleanError` whose JSON carries `removed` and `bytes_freed`.

Then each item is renamed into a `.staging/clean-<x>/` of its own (same file system); a scratch
item's lock is held across the rename of its directory and released before its lock file is removed
(Windows refuses to remove an open file), and a lock alone is held across its own rename except on
Windows.

A module whose last version went is renamed aside whole. At the end the `clean-<x>` directory is
deleted; `bytes_freed` subtracts what is left of it, which is a warning notice that the next clean
retries, as it removes a `clean-<x>` a killed clean left.

Module names and version ids read from disk are validated before they are joined into a path; every
listing uses `os.Lstat`, so a link is removed as a link and never followed, and sizes count regular
files only. A `path` is shown as it is on disk when it is plain text, which every validated name and
every name lola makes is, and through `untrusted.Text` on one line otherwise.

No other path holds the cache lock while it takes the markets or profiles lock (the cache lock is
taken only inside `Store`), so the markets-then-cache order cannot deadlock. A reader that loses a
version mid-copy fails its digest check, and its rerun fetches the version again, as after `lola
cache rm`.

Attribute files (`internal/module/attrs.go`) are untrusted data: read as regular files under 64 KiB,
YAML is built from `yamldoc.Decode`'s node tree by tag (only `!!str`, `!!bool`, `!!int`, `!!float`,
`!!null`, sequences and mappings; a float must be finite, and a plain number yaml.v3 leaves a string
because `ParseFloat` refuses it, such as `1e999`, refuses too), JSONC is `fsx.StandardJSON` (hujson,
comments and trailing commas blanked so offsets stay, nothing re-encoded) read token by token,
refusing a key given twice; one `attrRule` caps keys, strings, items and nesting in both.

Vars are
read the same way (`module.ParseVars` from `lola.yml`'s node and, through `manifest.EntryVars`, from
each entry); `planModule` checks an entry's overrides against the declaration (`checkVars`) before
anything renders and binds the declared vars with the overrides (`resolvedVars`).
