# `lola list`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola list [module...]
```

Alias: `ls`.

Each installed module × target with a status: `ok`, `modified` (a file lola wrote was
edited), `missing` (a file lola wrote, or the module copy, is gone), `stale` (`project.yml` pins
another version than the one installed: run `lola sync`), `unreadable` (the version installed is
cached but does not load), or `skipped` (nothing of the module could be rendered for that target),
followed by `→ update available` when your cache holds a newer version for the module's ref (`lola
sync --update <module>`);
and each target of an entry `project.yml` lists that is not installed,
`not installed` (`not_installed` in `--json` and `--plain`), with a notice per scope counting them
and naming the sync that installs them (`→ 1 module in project.yml is not installed → lola sync
--project`; the command repeats your `--json`, `--plain` and `--no-pager`); a `RULES` column says
whether the module's rules are on; and a `SKIPPED` column, shown when any module has some, counts the
components [`selections.yaml`](../guides/config-and-selections.md) skips.

## Which files drifted

A `modified` or `missing` status says how many of the files lola wrote for that target drifted:
`✗ missing 11/56`, `⚠ modified 2/56`; the count is of missing and modified files, not counting the
module copy. A row whose module copy is gone and which has no other missing file (an edited file
does not count) reads `✗ missing (module copy)`; with another file missing too it reads the count.

Name modules (`lola list caveman`) to show only them and, under the table, a block per drifted
row: its scope, module and target, then one line per drifted component, in full, missing before
modified:

```
user caveman claude-code:
    ✗ skill/caveman-learn (6 files)
    ✗ skill/caveman-setup (3 files)
    ⚠ skill/caveman (modified)
    ✗ module copy
→ restore: lola sync --user
→ keep out: lola install --user caveman --skip skill/caveman-learn --skip skill/caveman-setup
```

A file or setting the module's rules share, or one recorded before lola kept component names, shows
as its path (with the JSON pointer for a setting) instead. `restore` re-applies the module.
`keep out` appears when a missing component of a kind `--skip` takes (skill, command, agent, mcp,
rule) has a name, with one `--skip` per component; it stops installing those components (see
[`selections.yaml`](../guides/config-and-selections.md)) and the row then reads `ok`. The commands
carry the scope flags `list` read.

Bare `lola list` keeps one line per row; when any row drifted it adds, for each scope, `→ see what
changed: lola list --user caveman` naming the drifted modules. A named module installed in none of
the scopes read is an error with a `lola list` hint per scope read. `-t` still limits the targets. The `--user`,
`--project` and `--json` flags apply as they do without names, and a name completes from the
installed modules.

The `SOURCE` of a git module carries the ref `project.yml` records (`https://example.com/kit.git @ v1`;
the URL alone when it records none).

`--json` adds each
row's `pin`, `update_available`, `drift` and `recorded` (below). A module whose recorded source is a local folder or archive
that no longer exists shows `⚠ source gone` beside its status (`source_gone: true` in
`--json`); git and URL sources are never checked, as `list` reads no network.

`list` never reads the trust record: an entry you declined at the [trust question](../guides/sync-and-trust.md) `lola sync` asks shows as
`stale` or `not installed`.

Entries of
[profiles](../guides/profiles.md#profiles) are listed as `project.yml`'s own entries are, and when any module comes
from a profile a `FROM` column names its layer as the context line does: `default`, `project.yml`,
`profile go-review`, or for a member of a [bundle](../guides/bundles.md) on, `docs (bundle work)`; `--plain` and `--json`
keep `from` as `lock.json` records it (`profile:default`, `profile:user:docs`, `project.yml`), and `--plain` ends each row with the ref (empty when none) and then `update-available` when your cache has a newer version of it, `-` otherwise.
Without a scope flag it shows the project found from here, if any, and user scope; every row names
its scope ([Choosing a scope](../guides/choosing-what-to-install.md#choosing-a-scope)).
Like `lola sync`, it refuses a project whose `.lola/lock.json` git tracks, since a committed one could
turn profiles on.

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Only show these assistants. |

The global flags apply too ([Using lola](../usage.md#commands)): `--project`, `--user`, `-C/--root <dir>`
and `-s/--scope` pick the scope; `--json`, `--plain` and `--no-pager` pick the output. `--plain` prints
one tab-separated row per module and target: name, target, status, rules (`on` or `off`), source,
scope, from, ref.

Refusals: an unknown target, as `lola install` refuses it (`unknown target "nosuch"`, hinting `valid
targets: claude-code, gemini-cli, opencode, agents`); a tracked lock: `.lola/lock.json is tracked by git;
lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola
deletes; if you never installed with lola in this checkout, untrack and delete it, then run lola
sync --project`, hinting `git rm --cached .lola/lock.json`; when no flag chose the scopes and both
were found, it first hints `list the other scope: lola list --user`.

JSON: `modules` (each `scope`, `name`, `from`, `target`, `status`, `rules`, `source`, `ref`
(`""` when none), `digest`, `pin`, `update_available`, `skipped`, `only` (the `only:` list in
force for the module there, `[]` when none), `only_from` (the layer that sets it: `profile go`,
`default (base)`, `selections.yaml projects[<dir>]`; `""` when none) and `market` (the
marketplace the entry says it was installed through; absent when it names none), `recorded` (how
many files lola recorded for the module on that target) and `drift`) and `notices`. `drift` is always
present, `[]` when nothing drifted; each entry has `kind` (`skill`, `command`, `agent`, `mcp`,
`rule`, a runtime-hook or other kind, or `copy` for the module copy), `name` (the component;
`""` when lola recorded none), `path` (the first drifted path; the module copy's directory for
`copy`), `key` (a JSON pointer for a setting, else `""`), `status` (`missing` or `modified`) and
`files` (how many drifted files it covers). A component with both missing and modified files
gives one entry for each status. `--plain` rows have no drift lines.
