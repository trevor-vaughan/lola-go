# `lola list`

```
lola list
```

Alias: `ls`. Each installed module × target with a status: `ok`, `modified` (a file lola wrote was
edited), `missing` (a file lola wrote, or the module copy, is gone), `stale` (`project.yml` pins
another version than the one installed: run `lola sync`), `unreadable` (the version installed is
cached but does not load), or `skipped` (nothing of the module could be rendered for that target),
followed by `→ update available` when your cache holds a newer version for the module's ref (`lola
sync --update <module>`); and each target of an entry `project.yml` lists that is not installed,
`not installed` (`not_installed` in `--json` and `--plain`), with a notice per scope counting them
and naming the sync that installs them (`→ 1 module in project.yml is not installed → lola sync
--project`, keeping `--json`, `--plain` and `--no-pager`); whether the module's rules are on; and,
when any module has some, how many of its components `selections.yaml` skips. The `SOURCE` of a git module carries the ref `project.yml` records (`https://example.com/kit.git @ v1`;
the URL alone when it records none). `--json` adds each
row's `pin` and `update_available`. A module whose recorded source is a local folder or archive
that no longer exists shows `⚠ source gone` beside its status (`source_gone: true` in
`--json`); git and URL sources are never checked, as `list` reads no network.
`list` never reads the trust record: an entry you declined at sync's trust question shows as
`stale` or `not installed`. Entries of
[profiles](../guides/profiles.md#profiles) are listed as the manifest's are, and when any module comes
from a profile a `FROM` column names its layer as the context line does: `default`, `project.yml`,
`profile go-review`, or for a member of a bundle on, `docs (bundle work)`; `--plain` and `--json`
keep `from` as `lock.json` records it, and `--plain` ends each row with the ref (empty when none) (`profile:default`, `profile:user:docs`, `project.yml`).
Without a scope flag it shows the project found from here, if any, and user scope; every row names
its scope ([Choosing a scope](../guides/choosing-what-to-install.md#choosing-a-scope)).
Like `lola sync`, it refuses a project whose `.lola/lock.json` git tracks, since a committed one could
turn profiles on.

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Only show these assistants. |

Refusals: an unknown target, as `lola install` refuses it (`unknown target "nosuch"`, hinting `valid
targets: claude-code, gemini-cli, opencode`); a tracked lock: `.lola/lock.json is tracked by git;
lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola
deletes; if you never installed with lola in this checkout, untrack and delete it, then run lola
sync --project`, hinting `git rm --cached .lola/lock.json`; when no flag chose the scopes and both
were found, it first hints `list the other scope: lola list --user`.

JSON: `modules` (each `scope`, `name`, `from`, `target`, `status`, `rules`, `source`, `ref`
(`""` when none), `digest`, `pin`, `update_available`, `skipped`, `only` (the `only:` list in
force for the module there, `[]` when none), `only_from` (the layer that sets it: `profile go`,
`default (base)`, `selections.yaml projects[<dir>]`; `""` when none) and `market` (the
marketplace the entry says it was installed through; absent when it names none)) and `notices`.
