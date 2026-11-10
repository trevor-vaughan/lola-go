# `lola sync`

```
lola sync [<module>...]
```

Make the project (or `--user`) match its manifest: install every entry at the version it pins,
render the modules already installed at their installed versions, and remove the ones the manifest no longer lists. Names limit
the run to those modules. Shows and asks about changes to `project.yml` and its active [project
profiles](../guides/profiles.md#project-profiles) you have not trusted yet, entry by entry, paging the
diff on a terminal ([Sync, pins and trust](../guides/sync-and-trust.md#sync-pins-and-trust)). `--scope
both` syncs the project, then user scope.

Removals are the modules the manifest no longer lists, the assistants an entry no longer names,
and the files lola wrote that a version the run installs no longer ships (a component a new pin,
`--update` or a re-read folder dropped or renamed): `✗ <module>: <assistants> (not in the version
this run installs)` with the paths. They are listed under `-n` and in `--json` `removals`, asked
about once, before any hook question, on a terminal, and confirmed by `-y` or `--force`.

Two deletions are not removals and are never asked about; both are in the plan (`→ delete …`)
and in `-n`, and both are deleted, not moved to the trash. A JSON member (an MCP server, or a
runtime hook entry) that a module's new version drops goes without `-y`, because lola
cannot yet tell a dropped member from one it moved or re-keyed. Files dropped by your own edit of
`selections.yaml`, or by turning a module's rules off (`rules: false`), go too: the edit is your
decision (in `project.yml` that field is still a trust change). See [Safety
rules](../usage.md#safety-rules).

| Flag | What it does |
|---|---|
| `--trust[=<kinds>]` | Answer yes, without asking, to the questions of the kinds named that the run would ask: `entries` (the changes to `project.yml`, its top-level targets, and the pins and folder content a sync would write), `profiles` (the changes to its active project profiles, and a project profile taking your profile's place), `scripts` (the project's install scripts), `hooks` (a module's install hooks), `runtime-hooks` (a module's Claude Code runtime hooks and agent MCP servers), or `all`; bare `--trust` is `entries,profiles`, which run no code. It never bypasses a refusal ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)). |
| `-y`, `--yes` | Confirm removals, and only removals: it never trusts changes. |
| `--update` | Fetch the refs again and re-pin the modules of the run. |
| `--at <module>=<version>` | Install the module only at this version: a git module's commit (any case), or a folder's or archive's `sha256:<digest>` (the digest the diff prints). Repeatable; given twice alike it counts once; a module the run does not include is refused (with `--scope both`, only one no scope includes). A run given it applies only the versions it names: one its source no longer serves, and any other module whose version would change, are refused before anything is written. A refusal's hint adds one for each new version it showed (a pin it would write, a folder whose content changed), so the run it hints answers only what was listed. |
| `-v`, `--verbose` | Print every notice in full, grouped, instead of one line per module for those that are not about safety ([Output contract](../usage.md#output-contract)). |
| `--force` | Overwrite unowned or hand-edited files, render skipped artifacts that can be, and confirm removals. |
| `-n`, `--dry-run` | Print the plan and the trust diff, ask nothing and write nothing. |

Refusals:

- Changes to trust off a terminal: `lola sync needs your trust for .lola/project.yml, which has
  not been trusted on this machine, and there is no terminal to ask on` (after a pull, `your trust
  for changes to .lola/project.yml`), hinting `see them: lola sync --project -n` and `then
  accept them: lola sync --project --trust=entries` (the kinds missing: `--trust=profiles` for a
  project profile's changes, `--trust=entries,hooks` when a module's pre-install hook would refuse
  too).
  - An entry your trust record covers (`--update` of a module you installed, or a covered
    entry with no pin) is fetched into the run's private staging before the refusal, never into
    your cache, so the refusal shows its new version's files; an `--update` that finds nothing new
    needs no trust and is not refused.
  - The files a version read this way no longer ships, as your picks and settings would install
    it, are listed under `removals:`, and the message adds `and your confirmation to remove the
    files <module>'s new version drops`.
  - The accept hint carries `-y` only when every removal it would confirm is listed; that is the
    guarantee.
  - An entry a pull added or changed is fetched only after you trust it, and a source can fail
    to serve one (a notice says why, with its hints), so while one is installed and unread the
    hint has no `-y`, not even the `-y` you gave, and the message says `until it reads <module>,
    the removals listed may change, so -y waits until the next run lists them`; that run fetches
    it, lists every removal and hints `-y`.
  - A `-y` you type yourself on a hinted run confirms that run's removals, listed or not.
  - A failure of lola's own state (your cache, the staging directory) stops the run instead.
- Removals off a terminal: `lola sync needs your confirmation to remove my-module, and there is no
  terminal to ask on`, hinting `see them: lola sync --project -n` and `then confirm them: lola
  sync --project -y`. With the trust it needs given, the run first reads every version it would
  install, so the files they drop are listed too and `-y` confirms only what is listed.
- A local source outside the project, a pin whose content does not match, a conflict, and a
  tracked `.lola/lock.json` ([Sync, pins and trust](../guides/sync-and-trust.md#sync-pins-and-trust)).
- No manifest: `no project.yml in <dir>`. No scope found: as `lola install`'s, with hints for
  `--project`, `--user` and `--scope both`.

JSON: `scope`, `root`, `dry_run`, `in_sync`, `trust`, `pins`, `removals`, `rewrites`, `equivalent_commands`
and the plan's `modules`, `steps`, `findings`, `notices` and `scripts`; with `--scope both`, `{"scopes":
[...]}` ([Output contract](../usage.md#output-contract)). When sync refuses because it needs your trust or
your yes to removals, its error line adds `trust`, `removals` and `notices` (`[]` unless an entry
could not be read for the refusal: `<module>: could not read the version this run would install,
so the files it drops are not listed: <why>`, also printed above the diff; that entry's removals
are then unlisted, so the hint carries no `-y`).

Known limitations: when sync, or the pin it writes, meets another module of the entry's name in
your cache, the refusal hints `remove the cached one first: lola cache rm my-module` without
your run's `--json`, `--plain` or `--no-pager`, which `lola install` keeps. An entry with no targets (`my-module: project.yml gives it no targets`) hints advice,
not a command: `list the assistants in project.yml, for example targets: [claude-code]`
(`default.yml`, or the profile's file, where the entry comes from there).
