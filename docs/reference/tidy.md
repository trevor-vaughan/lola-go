# `lola tidy`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola tidy [<kind/name>...]
```

A `<kind/name>` selector names an item: kind `skill`, `command`, `agent`, `mcp` or `rule`, and a name
that may be a glob (`command/stray`, `'skill/old-*'`; [Selectors](../guides/tidying.md#tidying)).

Move what lola does not manage out of the assistants' directories and MCP server lists into the
trash (`$XDG_STATE_HOME/lola/trash/`, one directory per run; tidy never deletes): what you name,
`--all`, or on a terminal what you tick in a picker. Asks first on a terminal (`-y`); `-n` lists
what would move and what is locked ([never moved, whatever the flags](../guides/tidying.md#tidying); `protected` in JSON), and why. Your keep-list,
per scope in `keep.yaml`, names what tidy stops offering ([Tidying](../guides/tidying.md#tidying)).

| Flag | What it does |
|---|---|
| `--all` | Move every item that is not locked. |
| `-t`, `--target <assistant>` | Only tidy these assistants' directories (`claude-code`, `gemini-cli`, `opencode` or `agents`). |
| `--include-tracked` | Also offer what git tracks. |
| `--keep <selector>` | Add a `kind/name` selector to this scope's keep-list (repeatable). Alias: `--protect`. |
| `--unkeep <selector>` | Remove a selector from this scope's keep-list, as written there (repeatable). Alias: `--unprotect`. |
| `-y`, `--yes` | Do not ask for confirmation, and open no picker (not the scope picker either): the scope is the one found off a terminal (the `.lola/` project in the directory or above it, else `default_scope` in `config.yaml`), and a run without names or `--all` is refused as it is there. |
| `--force` | Also move what git tracks, without asking; names or `--all` are still needed. |
| `-n`, `--dry-run` | List what would move and what is locked, and change nothing. |

The global flags apply too ([Using lola](../usage.md#commands)): `--project`, `--user`, `-C/--root <dir>`
and `-s/--scope` pick the scope; `--json`, `--plain` and `--no-pager` pick the output.

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

`.agents/skills` is shared with other installers (Codex's skill installer, `npx skills`): tidy
looks there only when the scope's targets include `agents` (`-t agents`, or `agents` in the
manifest or the lock), and labels what lola did not install there `skills another tool installed`
(the candidate's `reason` in `--plain` and JSON).

JSON: `scope`, `root` (`""` at user scope), `dry_run`, `targets` (the assistants scanned), `trash`
(the run's directory; `""` when nothing moved), `size_bytes`, `candidates` (what would move, on a
dry run and in a refusal; else `[]`), `moved`, `protected` (locked items, which never move),
`skipped` (items left in place, each with a `code` and `reason`), `keep_added`, `keep_removed`,
`trash_credentials` (the trash paths of MCP file copies that may hold credentials), `notices` and
`hints`. Each item carries `target`, `kind`, `name` and `path`
([the guide](../guides/tidying.md#tidying) lists the rest).
