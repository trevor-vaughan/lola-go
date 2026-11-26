# `lola trash`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola trash [list]
lola trash show <run>
lola trash restore <run> [<original>...] [--force] [-n]
lola trash empty [<run>...] [--older-than <age>] [-n] [-y]
```

`lola tidy`, `lola migrate` and `lola profile rm` move what they take out of place into the
trash (`$XDG_STATE_HOME/lola/trash/`), one run per command; `install`, `sync`, `update` and
`uninstall` with `--force` copy each file they replace or delete there first, one run per scope. Each run holds `manifest.txt`, one
`original<TAB>trash path` line per entry, and `run.yaml`: the command that made it, its scope
(`user` or `project`), project (`root` in `--plain` and JSON; empty at user scope) and time, and the copies that may hold credentials. lola deletes what a run holds
only when you run `lola trash empty`; a restore that empties a run removes the run.

`lola trash list` (alias `ls`), and bare `lola trash` off a terminal, list the runs, newest first: the id (the run's directory name, what `<run>` and `<id>` stand for here), its
time, the command that made it, its scope and project, and its files and size. With no runs it
prints `✓ the trash is empty`. A run made before lola wrote `run.yaml` shows the command
`unknown`.

`lola trash show <run>`, or `lola trash <run>`, shows the run's line, then each entry as
`<original> → <trash path>`, and ends with `→ restore them: lola trash restore <run>`. When an original is there now (always
so for the config file a tidy copied), that restore would be refused, so the hint is the one that
works: `→ restore them (replaces <path>, which moves to a new trash run first): lola trash restore <run>
--force`; `--json` lists those paths as `existing`.

An entry that is a copy of an MCP or
settings file, or a directory holding one, is marked `(may hold credentials)`.

On a terminal, bare `lola trash` (without `--json` or `--plain`) opens a picker instead of the
list (`lola trash list` never does): each run, newest first, as a `[whole run]` row of its time, command, scope and project,
files and size, followed by its files, indented, each path relative to the run's project (whole
when the run has none), a copy that may hold credentials marked `(may hold credentials)` before its
path.

`x` ticks a row; `/` filters; Enter moves on to `Do what with the ticked?`: `Restore ticked`
or `Empty ticked runs`; `q` or Ctrl+C quits, changing nothing.

A ticked `[whole run]` row takes the
whole run, whatever of its files is ticked; its files' rows stay as they are. The key help under
the picker says what each key does. Nothing is written before lola shows the plan:

- `Restore ticked` shows, for each run, the plan `lola trash restore <run> [<original>...] -n`
  prints (a run ticked whole names no original), then offers `Restore`.
  - When an original exists, the only choice offered is `Restore with --force: first move what is
    there into a new trash run`, and lola asks once more before moving anything.
  - Cancel goes back to the picker, the ticks kept, as Esc does at either question; Ctrl+C ends
    the run, changing nothing.
  - Two ticked runs that would restore the same path (or one inside the other) send you back to
    the picker, naming the path and both runs, before any plan is shown.
  - When nothing ticked is left in the trash, lola says `nothing to restore, so nothing changed`.
- `Empty ticked runs` deletes the runs whose `[whole run]` row is ticked, as `lola trash empty <run>...`
  does: it shows the plan and asks; Esc there goes back to the picker, the ticks kept.
  Ticking only files shows `Empty ticked runs deletes whole runs: tick a [whole run] row, not only
  files` and asks the picker again, the ticks and `Empty ticked runs` kept.

Off a terminal, bare `lola trash` lists the runs as above.

| Subcommand                          | What it does                                                                                                                                                                  |
|-------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `list` (alias `ls`)                 | List the runs, newest first, as text.                                                                                                                                         |
| `show <run>`                        | Show a run's entries. `lola trash <run>` is the same.                                                                                                                         |
| `restore <run> [<original>...]`     | Move the run's entries back, or only the originals you name (a relative path is taken from the working directory), making the directories they were in. Aliases: `undo`, `recover`. |
| `empty [<run>...]`                  | Delete the named runs, or, with none named, every run. It asks first; off a terminal it needs `-y`. Aliases: `purge`, `clear`.                                                |

| Flag                 | Of      | What it does                                                                                                                                                              |
|----------------------|---------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--force`            | restore | First move what is at an original's path into a new trash run, whose command is `lola trash restore <run> --force`. Nothing is deleted.                                   |
| `--older-than <age>` | empty   | Only the runs older than `<n>d` or `<n>w` (weeks of 7 days). A run whose time lola cannot tell, or a run you named that is newer, is kept, with a notice.                 |
| `-n`, `--dry-run`    | restore, empty | Print the plan and change nothing.                                                                                                                                 |
| `-y`, `--yes`        | empty   | Do not ask for confirmation.                                                                                                                                              |

The global flags apply too ([Using lola](../usage.md#commands)): `--json` and `--plain` pick the output,
`--no-pager` prints without lola's viewer.

A restore never writes over what is there: when an original exists, the whole run is refused
(`-n` lists such paths as `⚠ … exist, so the restore is refused`). A link comes back as a link.

A credential copy comes back at mode 0600, as does a marked file inside a directory entry. The
lines of the restored entries leave `manifest.txt`, and a run that then lists nothing is removed.

When an entry is back but lola cannot make a credential copy in it private, the restore still
counts it and warns `restored <path>, but cannot make <path> private (mode 0600): …`.

`lola tidy` keeps a copy of a whole config file (`.mcp.json`, a settings file) when it removes an
MCP server from it, and the file itself stays; so does a `--force` run that writes over or removes
an entry in one that lola did not write or you edited. Restoring that copy with `--force` replaces the
whole file, so every change made since goes with it, MCP servers lola installed later included
(`lola sync` puts those back).

The plan (what `-n` prints, and what the picker shows before `Restore`) says `⚠ the restore replaces the whole file
<path> with the copy trash run <run> holds, at mode 0600 (it may hold credentials); the file there
now, with every change made since, moves to a new trash run first`, and the restore says
`⚠ replaced the whole file <path> with the copy from trash run <run>, at mode 0600; the file that
was there is in trash run <id>`.

To keep the later changes, restore nothing and copy the server you
want from the trash path that `lola trash show <run>` lists.
`empty` deletes only what it planned: a run whose files or size changed since the plan (a
lola still writing it) is kept, with `trash run <id> changed since the plan, so it is kept;
rerun: lola trash empty …`.
An entry gone from the trash is passed over with a warning, and its line is dropped. A run's
files are untrusted data: a manifest line whose trash path lies outside the run, or whose
original is not absolute, is refused with a warning, and `run.yaml` or `manifest.txt` over 1 MiB
is not read.

Refusals:

- `there is no trash run <id>`, hinting `see the runs: lola trash list`.
- `trash run <id> does not hold <path>`, hinting the run's entries: `lola trash show <id>`.
- `lola trash restores nothing over what is there: <paths> exist`, hinting `lola trash restore <run> --force`.
- `trash run <id> holds <a> and <b>, which one restore cannot both put back; restore one at a
  time`, when two entries share an original or one lies inside the other.
- `--older-than "<age>" is not an age; …` (`must be at least 1d` for a zero age), hinting the run with `--older-than 30d` in its place and
  without `-y`: a hint never drops the age, which would delete every run.
- `lola trash restore needs a trash run` (likewise `show`), hinting `find one: lola trash list`;
  `lola trash shows one run at a time` for `lola trash <run> <run>`, hinting `show one` and
  the restore of what was named.
- An old action flag (`--restore`, `--undo`, `--recover`, `--empty`, `--purge`, `--clear`):
  `--restore is not a flag of lola trash`, hinting the subcommand with every word you typed and
  the flags it takes (`lola trash restore <run> -n`); when it would refuse them, its usage and
  `lola trash list`. A flag of the other subcommand (`--older-than` on `restore`, `--force` on
  `empty`): `--older-than applies to lola trash empty`, hinting that command's usage with the flag
  and its value (`usage: lola trash empty [<run>...] --older-than 30d`), not your operands.
- A first word near a subcommand that names no run (`lola trash restor <run>`):
  `unknown trash run or command "restor"`, hinting `did you mean: lola trash restore <run>`.
- Off a terminal, `empty` without `-y`: `lola trash empty needs confirmation, and there is no
  terminal to ask on`, hinting the run with `-y`.

`--plain` rows: a listing prints `run`, id, time, command, scope, root, files and bytes; showing
a run adds `entry`, original, trash path and `true` or `false` (may hold credentials). A restore
prints `restore` (`-n`) or `restored` with the original and trash path, `exists` with an original
in the way, `displaced` with the new run's id, and `removed` with the run's id when it was
removed. An empty prints `delete` (`-n`) or `deleted` with the run's id and bytes.

JSON: the listing has `runs` (each `id`, `time` (RFC 3339, UTC; `""` when unknown), `command`,
`scope`, `root`, `files` and `size_bytes`) and `notices`. Showing a run gives the run's keys,
`entries` (each `original`, `trash` and `credentials`), `existing` (the originals there now) and `notices`. A restore gives `run`,
`restored` (entries), `existing`, `displaced` (`""` for none), `run_removed`, `dry_run` and
`notices`. An empty gives `removed` (runs), `bytes_freed`, `dry_run` and `notices`, and its
refusal off a terminal carries the same keys beside `error` and `hints`. Every text a run
supplies is printed with control and format characters replaced, in JSON too.
