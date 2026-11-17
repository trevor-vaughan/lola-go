# `lola update`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola update [<module>...]
```

Alias: `upgrade`. Move every installed module (those named, if any) to the version its source holds
now and render it. The sources are the ones `project.yml` and its active
[profiles](../guides/profiles.md#profiles) name; a [marketplace](../guides/marketplaces.md#marketplaces)
module follows its catalog, and `update` fetches the catalogs on every run. A ref fetched less than
`cache_fresh_for` ago (`config.yaml`; one day unless it says otherwise) is served from your cache
with no network contact; `--fetch` fetches every ref now. A ref whose fetched head (its tip) has no fetch time
recorded counts as stale, and a folder source is read on every run.

`update` installs and removes no module. A module no layer lists any more is left as
installed with a `not-in-manifest` warning hinting `lola sync`, which would remove it. A module
whose entry asks for other assistants than the ones installed is left as installed, with a
`targets-differ` finding; `lola sync` applies the change. An entry that comes from a remote
profile is left too, with a notice to run `lola profile <name> --update`. A module named but not
installed in the scope is refused. To render again at the installed versions, fetching nothing,
run `lola sync`.

A new version may no longer ship a file lola wrote for the old one: a component it dropped or
renamed. Those files are removals, listed before anything is written under `To remove:`, as a dry
run's report lists them (`Removed:` in a run's), with `✗ <module>: <assistants> (not in the version
this run installs)` and the paths, and in `--json` `removals` (each with `why`). On a terminal
`update` asks; off one it is refused (`lola update needs your confirmation to remove the files
<module>'s new version drops, and there is no terminal to ask on`), hinting the run with `-n` and
with `-y`.

`-y` or `--force` confirms them (`--force` answers only this destructive question, never a consent
question); `-n` lists them and writes nothing.

A JSON member (an MCP server, or a runtime hook entry) the new version no longer ships is not a
removal: `update` deletes it from the JSON file without asking, as `lola sync` does, because lola
cannot yet tell a dropped member from one it moved or re-keyed. It is in the plan (`→ delete …`)
and in `-n`, and it does not go to the trash ([Safety rules](../usage.md#safety-rules)).

Trust is `lola sync`'s ([Sync, pins and trust](../guides/sync-and-trust.md#sync-pins-and-trust)): a
fresh head that has not moved asks nothing, so within `cache_fresh_for` a run with nothing new
works without a terminal and without `--trust`. A stale head (or every head, with `--fetch`) is
fetched first, into the run's private staging, never into your cache: a fetch that finds nothing
new asks nothing, off a terminal too.

A pin that moved is shown with its files and asked about,
or answered by `--trust`. Off a terminal without `--trust` the run is refused once, writing
nothing (your cache, `project.yml`, `lock.json`, the trust record and your files stay as they
were), naming the pins it would write and, when the new version drops files, the confirmation
they need: `lola update needs your trust for the pins it would write for example; and your
confirmation to remove the files example's new version drops, and there is no terminal to ask
on`, hinting `then accept them: lola update --project --fetch --trust=entries --at
example=<commit> -y`, which applies in one run.

`--at` pins each new version the refusal showed:
that run fetches the branch again, and if it now serves another commit, it is refused, writing
nothing (`example: its source now serves <commit>, not <commit> as --at says; nothing was
written`), hinting `see what it serves now:` with `-n`; one whose source no longer serves that
version says so (`its source does not serve <commit>, which --at names`).

A run given `--at`
applies only the versions a refusal showed: any other module whose installed version would
change (its source moved after the refusal, a pin a pull wrote, or one a refusal could not read)
is refused before anything is written (`<module> has a new version no refusal showed, and --at does not pin it`), hinting
`see what changed:` with `-n`, whatever its `--trust`, `-y` or `--force` say. A printed hint
carries `-y` only when every removal it would confirm is listed; that is the guarantee.

While
an installed entry is unread (lola has not read its new version: a pull changed it and nothing is
fetched before you trust it, or its source failed to serve it, which a notice names), the hint has none, even if you gave `-y`, the message says `until it reads
<module>, the removals listed may change, so -y waits until the next run lists them`, and the
run it hints lists every removal and asks for `-y` then. A `-y` you type yourself on a hinted
run confirms that run's removals, listed or not. In CI, pass `--trust` with the kinds you accept
(`--trust=entries` for pins alone). `--force` never answers a consent question.

| Flag | What it does |
|---|---|
| `--fetch` (alias `--refresh`) | Fetch every ref now, however recently it was fetched. |
| `--at <module>=<version>` | Install the module only at this version: a git module's commit (any case), or a folder's or archive's `sha256:<digest>` (the digest the trust diff shows). Repeatable; given twice alike it counts once. A run given it applies only the versions it names: one its source no longer serves, and any other module whose version would change, are refused, writing nothing. A refusal's hint adds one for each new version it showed. |
| `-y`, `--yes` | Open no scope picker (the scope is the one found off a terminal, or the run is refused as it is there), and confirm removing the files a new version no longer ships; it never trusts a change. |
| `--trust[=<kinds>]` | Answer yes, without asking, to the questions of the kinds named that the run would ask, as `lola sync --trust` does (bare: `entries,profiles`); it never bypasses a refusal ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)). |
| `--unseen` | Let `--trust`'s `scripts`, `hooks` and `runtime-hooks` consent to content lola has not shown you too; without it they consent only to what an earlier output showed you in full ([What lola has shown you](../guides/install-trust-kinds.md#what-lola-has-shown-you)). For CI and fresh machines. Refused without one of those kinds in `--trust`; `--force` never implies it. |
| `--force` | Overwrite unowned or hand-edited files and render skipped artifacts that can be; never answers a consent question. |
| `--strict` | Fail, writing nothing, if any target would drop, change or skip part of a module. |
| `-v`, `--verbose` | Print every notice in full, grouped, instead of one line per module for those that are not about safety ([Output contract](../usage.md#output-contract)). |
| `-n`, `--dry-run` | Print the plan and the trust diff, ask nothing and write nothing. At project scope nothing is written; at user scope a dry run may still publish what it fetched into your cache. |

It takes no `-t` (each entry says its own assistants).
Scope is [`lola sync`'s](sync.md): `-C`, `--user` or `--scope project|user|both` (hints name the
scope they run in: `--project` for the project found from where you are); `--scope both` updates the
project, then user scope, and stops at the first that fails or is declined.

Output says `updated <module>` for each module it moved or re-rendered, `already up to date` when
nothing changed, and `<module>: <from> → <to>` for each pin that moved. Two info notices say what
it did not fetch or install:

- `N modules served from your cache (fetched within cache_fresh_for) → lola update … --fetch`,
  when any ref was served from a fresh head instead of fetched; the command is your run with
  `--fetch`. A `--fetch` run has none.
- `N modules are listed but not installed: <names> → lola sync <names>`, for the entries a layer
  lists that are not installed, which `update` never installs; `lola sync <names>` installs them.
  A module installed but no longer listed has its `not-in-manifest` warning instead.

The commands in both notices keep your scope and output flags, and `-n` for a dry run.

Refusals: a named module that is not installed in the scope, hinting `lola sync <name>` when
`project.yml` lists it and `lola install <name>`; a hand-edited file: `refusing to change
anything: 1 conflict, which --force overrides`, hinting the run with `--force`; a tracked
`.lola/lock.json`, as [`lola list`'s](list.md); changes to trust off a terminal, as `lola sync`'s.

A module whose fetch fails stays installed, and is reported as a `fetch-failed` warning hinting
`lola update <name> --fetch`, after what the fetch itself says to fix first (the refs the
repository has, how to reach it); `update` brings up to date the modules it can reach and then
exits `1`.

A folder module whose source folder is gone is rendered at the version installed, and is not
reported as up to date: its `source-gone` warning says `<module>: source gone; not checked`, with
the folder, and hints restoring it or `lola uninstall <module>` (your scope flags kept). `update`
exits `1`, as for a failed fetch, and does not print `already up to date`.

Exit status is `0` on success, on a dry run and when you decline a prompt, and `1` on any error,
a refusal included ([Output contract](output-contract.md)).

JSON: the shape of `lola sync`'s: `scope`, `root`, `dry_run`, `in_sync`, `trust`, `pins`,
`removals`, `equivalent_commands` and the plan's `modules`, `steps`, `findings`, `notices` and
`scripts`; arrays are `[]` when empty. A failed fetch appears only as a finding with the code
`fetch-failed`, or `source-gone` for a vanished folder.
