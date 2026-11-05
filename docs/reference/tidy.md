# `lola tidy`

```
lola tidy [<kind/name>...]
```

Move what lola does not manage out of the assistants' directories and MCP server lists into the
trash (`$XDG_STATE_HOME/lola/trash/`, one directory per run; tidy never deletes): what you name,
`--all`, or on a terminal what you tick in a picker. Asks first on a terminal (`-y`); `-n` lists
what would move and what is locked (never moved, whatever the flags), and why. Your keep-list,
per scope in `keep.yaml`, names what tidy stops offering ([Tidying](../guides/tidying.md#tidying)).

| Flag | What it does |
|---|---|
| `--all` | Move every item that is not locked. |
| `-t`, `--target <assistant>` | Only tidy these assistants' directories. |
| `--include-tracked` | Also offer what git tracks. |
| `--keep <selector>` | Add a `kind/name` selector to this scope's keep-list (repeatable). Alias: `--protect`. |
| `--unkeep <selector>` | Remove a selector from this scope's keep-list, as written there (repeatable). Alias: `--unprotect`. |
| `-y`, `--yes` | Do not ask for confirmation. |
| `--force` | Also move what git tracks, without asking; names or `--all` are still needed. |
| `-n`, `--dry-run` | List what would move and what is locked, and change nothing. |

Refusals:

- Off a terminal (or with `--json`) without names or `--all`: `lola tidy needs names or --all
  when it cannot ask (off a terminal or with --json): 1 item can move`, hinting `lola tidy
  --project --all -y`.
- With them but without `-y`: `lola tidy would move 1 item: command/stray
  (.claude/commands/stray.md); it cannot ask (off a terminal or with --json), so it needs -y to
  move them`, hinting the run with `-y`.
- A scope with no targets and nothing installed: `project <dir> lists no targets and lola
  installed nothing there`, with one hint per installed assistant (`lola tidy --project -t
  claude-code`).
- No scope found: `lola tidy needs a scope: no .lola/ project was found in <dir> or above it, and
  <config.yaml> sets no default_scope (default_scope: project or user)`, hinting `lola tidy
  --project` and `lola tidy --user`, each with the run's other flags.

JSON: `scope`, `root`, `dry_run`, `targets`, `trash`, `size_bytes`, `candidates`, `moved`,
`protected`, `skipped`, `keep_added`, `keep_removed`, `trash_credentials`, `notices` and `hints`.
