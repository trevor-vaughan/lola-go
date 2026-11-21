# `lola migrate`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola migrate
```

Move from the Python lola: install what it installed, with lola, and clean up after it ([Coming from
the Python lola](../guides/migrating.md#coming-from-the-python-lola) says what is installed, replaced
with a backup or moved to the [trash](trash.md)). It shows the plan and asks (default No).

| Flag                         | What it does                                                                                                                                                                                                                                                                                                                                                                   |
|------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--keep-python`              | Install, and replace with a backup, but move nothing else to the trash.                                                                                                                                                                                                                                                                                                        |
| `-y`, `--yes`                | Do not ask for confirmation.                                                                                                                                                                                                                                                                                                                                                   |
| `--force`                    | Also replace, after backing each up into the trash, unowned files outside the recorded paths, recorded files git tracks or that lie below a symbolic link, and module copies; and move files git tracks, without asking; it answers the question as `-y` does, never hook consent (what each is: [the guide](../guides/migrating.md#coming-from-the-python-lola)).             |
| `--trust[=<kinds>]`          | Consent to the kinds named without asking, recorded as if you had answered yes at the prompt; bare `--trust` means `entries`. `entries` (components that changed upstream since the Python lola installed them, below) only lifts that gate and records nothing; the others are recorded: `hooks` (install hooks), `runtime-hooks` (Claude Code runtime hooks), `scripts`, or `all` for the four ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)).             |
| `--unseen`                   | Let `--trust`'s `scripts`, `hooks` and `runtime-hooks` consent to content lola has not shown you too; without it they consent only to what an earlier output showed you in full ([What lola has shown you](../guides/install-trust-kinds.md#what-lola-has-shown-you)). For CI and fresh machines. Refused without one of those kinds in `--trust`; `--force` never implies it. |
| `-n`, `--dry-run`            | Print the plan and write nothing.                                                                                                                                                                                                                                                                                                                                              |
| `--map-root OLD=NEW`         | Look for a project path or local source this machine lacks, below `OLD`, at the same place below `NEW` (repeatable; [From another machine](../guides/migrating.md#from-another-machine)). A map that matches nothing is a warning (the `warnings` JSON key).                                                                                                                   |
| `--map-target OLD=NEW[,NEW]` | Install the modules of `OLD`, an assistant this machine lacks, for the assistants `NEW` instead (repeatable; name each `OLD` once).                                                                                                                                                                                                                                            |

On a terminal, without `-y`, it asks for each missing root and each missing assistant before it
plans. While it plans and previews, a terminal's last line says what it is
working on (`… fetching https://github.com/acme/kit`, `… planning project /home/me/proj (3 of 41)`),
and is cleared before the plan prints; with `LOLA_ACCESSIBLE=1` each step is a line of its own, and
off a terminal, or under `--json` or `--plain`, nothing is printed.

The run ends with `next steps:`, everything you can act on, each once: the consent it skipped, and
the command that maps what it left:

```
→ map what this machine lacks: lola migrate --map-root /Users/alice=/home/alice --map-target claude-code=opencode
```

A branch or unpinned source installs at its current head. Each component that differs from, or is
absent in, the Python lola's copy of the module (`modules/<name>` in its home; every component when
the copy is gone) is marked `changed upstream since you installed it` in the plan, and a new MCP
server is listed with the command it runs. A tag, a full commit SHA, and a folder or archive on your
machine are never marked. `lola migrate -y` without `--trust=entries` exits 3 and writes nothing,
hinting `lola migrate -n` to see what changed and the run with `--trust=entries`; on a terminal it
asks `Install them?` after `Migrate?`. `--force` does not answer it.

Refusals: off a terminal without `-y`: `lola migrate needs confirmation, and there is no
terminal to ask on`, hinting `lola migrate -y`. A map it cannot read says what is wrong:
`--map-root "alice=/home/alice": "alice" is not an absolute path`, `--map-target
"claude-code=vim": "vim" is not an assistant lola installs for; write claude-code, gemini-cli,
opencode, agents`.

A run that finishes with records left as they are (the summary's `left`) prints its report as
usual and exits `5`; a fully migrated run exits `0`, a failed destination `1`, and the refusals
above `3`, which wins over `5`. A dry run exits `0`. A `$LOLA_HOME` that does not exist is an error
(`LOLA_HOME=<path> does not exist`, hinting to unset or fix it), unlike a missing `~/.lola`, which
is `nothing to migrate`.

For a Cursor, Copilot or OpenClaw record, the hint that maps it names `agents` among the targets;
`agents` takes skills only, so the record's commands and agents are skipped. Repeating
`--map-root OLD` is refused as repeating `--map-target OLD` is. A `~/.mcp.json` entry stays, with a
hint: Claude Code reads that file only for sessions started in `~`; when the run wrote the server
to `~/.claude.json`, the hint says so (a dry run, which writes nothing, does not).

JSON: `dry_run`, `nothing_to_migrate`, `python_home`, `trash`, `destinations`, `unmappable`,
`not_migrated` (these two list every left record, those under a destination too, which keep their
own), `markets`, `home`, `warnings`, `trusted` (what `--trust` covered, each item once;
under `-n`, what it would cover), `summary` and `remap`; each install lists its `changed_upstream` components ([Output
contract](../usage.md#output-contract)); the refusal off a terminal carries the `-n --json` keys beside
`error` and `hints`.
