# `lola migrate`

```
lola migrate
```

Move from the Python lola: install what it installed, with lola, and clean up after it ([Coming from
the Python lola](../guides/migrating.md#coming-from-the-python-lola)). It shows the plan and asks
(default No).

| Flag | What it does |
|---|---|
| `--keep-python` | Install, and replace with a backup, but move nothing else to the trash. |
| `-y`, `--yes` | Do not ask for confirmation. |
| `--force` | Also replace, after backing each up into the trash, unowned files outside the recorded paths, recorded files git tracks or that lie below a symbolic link, and module copies; and move files git tracks, without asking. |
| `--trust=<kinds>` | Consent without asking to the kinds named, recorded as a yes at the prompt is: `hooks` (install hooks), `runtime-hooks` (Claude Code runtime hooks), `scripts`, or `all` for the three ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)). |
| `-n`, `--dry-run` | Print the plan and write nothing. |
| `--map-root OLD=NEW` | Look for a project path or local source this machine lacks, below `OLD`, at the same place below `NEW` (repeatable; [From another machine](../guides/migrating.md#from-another-machine)). A map that matches nothing is a warning. |
| `--map-target OLD=NEW[,NEW]` | Install the modules of `OLD`, an assistant this machine lacks, for the assistants `NEW` instead (repeatable; name each `OLD` once). |

On a terminal, without `-y`, it asks for each missing root and each missing assistant before it
plans. While it plans and previews, a terminal's last line says what it is
working on (`… fetching https://github.com/acme/kit`, `… planning project /home/me/proj (3 of 41)`),
and is cleared before the plan prints; with `LOLA_ACCESSIBLE=1` each step is a line of its own, and
off a terminal nothing is printed. The run ends with `next steps:`, everything you can act on, each once: the consent it skipped,
the command that maps what it left (`→ map what this machine lacks:
lola migrate --map-root /Users/alice=/home/alice --map-target claude-code=opencode`).

Refusals: off a terminal without `-y`: `lola migrate needs confirmation, and there is no
terminal to ask on`, hinting `lola migrate -y`. A map it cannot read says what is wrong:
`--map-root "alice=/home/alice": "alice" is not an absolute path`, `--map-target
"claude-code=vim": "vim" is not an assistant lola installs for; write claude-code, gemini-cli,
opencode`.

JSON: `dry_run`, `nothing_to_migrate`, `python_home`, `trash`, `destinations`, `unmappable`,
`not_migrated`, `markets`, `home`, `warnings`, `summary` and `remap` ([Output
contract](../usage.md#output-contract)); the refusal off a terminal carries the `-n --json` keys beside
`error` and `hints`.
