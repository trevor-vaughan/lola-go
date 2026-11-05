# `lola cache`

```
lola cache
```

Manage your cache ([Sources and the cache](../usage.md#sources-and-the-cache)). Bare `lola cache` lists it,
as `lola cache list` does. Removing a module from the cache does not touch installed files.

## `lola cache add`

```
lola cache add <source|@market/module>
```

Alias: `get`. Fetch a module into your cache without installing it. `@market/module` fetches an
entry of one of your marketplaces as `lola install` would, its commit pin checked against its
ref (`--path` is refused: the catalog gives it).

| Flag | What it does |
|---|---|
| `--ref <ref>` | The git branch, tag or full commit SHA to fetch. |
| `--path <dir>` | The module root inside the source (monorepos). |

Refusals: no source: `lola cache add needs a source`.

The cache keeps a module by its name and the source it came from: adding a source of a name the
cache holds from another source keeps both. A command that names a module and no source (`lola
install <name>`, `lola info <name>`, `lola cache list`) uses the source cached most recently; an
entry in `project.yml` or a profile always uses its own. `lola cache rm <name>` removes every
source's copy.

JSON: the module's identity and contents, as [`lola info`](info.md#lola-info) starts its document, with
`path` when `--path` gave one.

## `lola cache rm`

```
lola cache rm <module>...
```

Aliases: `remove`, `uninstall`, `erase`, `delete`, `del`. Remove every cached version of the
modules. Installed modules keep working: each one runs from its own module copy, and `lola
sync` fetches the source again when it needs to. Asks first.

| Flag | What it does |
|---|---|
| `-y`, `--yes` | Do not ask for confirmation. |

Refusals: `nope is not in your cache`, hinting `cached modules: lola cache list`; off a terminal
without `-y`: `lola cache rm needs confirmation, and there is no terminal to ask on`, hinting the
run with `-y`.

JSON: `removed`, the names. The refusal off a terminal carries `would_remove`, the names it
would have removed, beside `error` and `hints`.

## `lola cache clean`

```
lola cache clean [--unused-for <age>] [--all] [-n] [-y]
```

Remove what your cache no longer needs. Asks first.

| Form | Removes |
|---|---|
| bare | Superseded versions (each version no head points to: a newer fetch of its ref replaced it) and scratch: each `.staging/run-*` and `tmp/check-*` that no running lola holds, a `run-*.lock` or `check-*.lock` left without its directory, and a `.staging/clean-*` a killed clean left. Every module keeps its heads. With `cache_unused_for` set in `config.yaml`, also what `--unused-for` with that age removes. |
| `--unused-for <n>d` or `<n>w` | Also every version no run used for at least that many days (`1w` is 7 days), current heads included: a head whose version goes is dropped, and a module whose last version goes is removed whole. |
| `--unused-for never` | What bare removes, without the configured age. |
| `--all` | Every module, every marketplace catalog and all scratch, except scratch a running lola holds. The cache's lock file and `probes.json` stay. |

| Flag | What it does |
|---|---|
| `--unused-for <age>` | `<n>d` (days), `<n>w` (weeks) or `never`; not with `--all`. |
| `--all` | Remove everything but held scratch; not with `--unused-for`. |
| `-n`, `--dry-run` | Print what would be removed and remove nothing. |
| `-y`, `--yes` | Do not ask for confirmation. |

A version is used when a run installs it (`install`), fetches it (an `install` that applies, or
`info`, of a source, `cache add`, `cache update`, `sync`, `update` fetching it again, `profile
--update`) or renders the version a project records (`sync`, `update`, `profile --on`, `--switch`,
`--update`, their dry runs included); `index.yml` records the day (UTC) as `last_used`, at most
once a day.

`lola info` and `install -n` of a cached name, `cache list`, `check`, `list` and
`cache clean -n` record nothing. A version cached before lola recorded uses counts as used the
day a clean first sees it, as does one whose `last_used` entry is not a day lola wrote.

A cache
whose `index.yml` lola cannot write (a read-only cache) still installs and renders; the use is
not recorded.

On a terminal clean prints each item with its reason and size, and the total, then asks `Remove
these from your cache?`; under `-n` it prints the same and asks nothing. With nothing to remove it
prints `✓ nothing to clean` and asks nothing. Each item is checked again before it goes: one that
changed since the plan (a head moved onto it, a run used it, it is gone, a directory above it is no
longer a real directory) is kept and reported; when every item changed, clean prints `→ nothing
removed`.

A module whose `index.yml`, or an entry, clean
cannot read, when it plans or when it checks again, is kept as `unreadable`, and the clean goes
on.

`index.yml` stops naming a version
before the version is removed, and a clean that fails part way puts back the heads of the
versions it did not remove, so no head names a removed version; its error (under `--json`, with
`removed` and `bytes_freed`) says what it had removed. Clean sets items aside only in a
`.staging` that is a real directory. What a clean cannot delete after setting it aside is a
warning, `bytes_freed` counts only what was deleted, and the next `lola cache clean` retries it.

Risk: removing a version a project records makes that project's next `sync` fetch it again,
and a source that moved or is gone fails as it does after `lola cache rm`. Removing catalogs
(`--all`) leaves your marketplaces registered but not fetched: installing from one refuses with
`marketplace <name> is not fetched: its catalog is not in your cache`, hinting `lola market <name>
--update`. `lola sync --update` fetches again only the catalogs of marketplaces that entries
installed in the scope came from; for any other, run `lola market <name> --update`.

Refusals: `--all` with `--unused-for` (cobra's `if any flags in the group [all unused-for] are set
none of the others can be`); an age that is not one: `--unused-for "3x" is not an age; write a
whole number of days or weeks, such as 30d or 2w, or never` (`--unused-for ""` too, which never
falls back to the configured age; in `config.yaml`, with its file, line and key); off a terminal
without `-y`: `lola cache clean needs confirmation, and there is no terminal to ask on`, hinting
the run with `-y`.

JSON: `removed` (each `module`, `version`, `reason`, `path`, `bytes`), `kept` (each `module`,
`version`, `reason`, `path`), `bytes_freed`, `dry_run` and `notices`. `reason` is `superseded`,
`unused`, `scratch` or `all` for a removed item, `in use` (scratch a running lola holds),
`changed` or `unreadable` for a kept one.

`module` and `version` are `""` for scratch and
catalogs, and `version` is `""` for a module `--all` removes; `path` is relative to
`$XDG_CACHE_HOME/lola`, as it is on disk when it is plain text, with control and format
characters replaced otherwise.

The refusal off a terminal carries the keys `-n --json` prints
beside `error` and `hints`. `--plain` rows: `remove` (under `-n`), `removed` or `kept`, then the
reason, the path and the bytes, and a `notice` row per notice.

## `lola cache list`

```
lola cache list
```

Alias: `ls`. List the modules in your cache.

JSON: `modules` (each the identity and contents of [`lola info`](info.md#lola-info)) and `warnings`.

## `lola cache update`

```
lola cache update [module...]
```

Fetch every ref of the named modules again, or of every module if none is named. Installed
modules keep the versions they were installed at; `lola install <module>` installs the new one.

Refusals: `nope is not in your cache`, hinting `cached modules: lola cache list`.

JSON: `updates` (each `name`, `ref`, `from`, `to`, `changed`) and `notices`.

## `lola cache search`

```
lola cache search <query>
```

Alias: `find`. Search your cache alone, as `lola search` searches it.

Refusals: no query: `lola cache search needs a query`.

JSON: as `lola search`'s.
