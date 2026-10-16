# Using lola

`lola help` is the complete, always-current reference (piped, it prints every command with its
aliases and flags). This page explains the concepts behind it. It describes this build; planned
changes are marked "planned" and tracked in [roadmap.md](roadmap.md).

## Commands

| Command | What it does |
|---|---|
| `lola install <source\|name>...` | Copy the module into your cache (if it is a source), render it for each target, record it in the manifest. |
| `lola uninstall <module>...` | Remove everything lola wrote for the module. Asks first; `-y` skips the question. |
| `lola list` | Each installed module × target with a status: `ok`, `modified` (a file lola wrote was edited), `missing` (a file lola wrote is gone), or `stale` (your cache has a newer version). |
| `lola render` | Render every installed module again from your cache. Removes files a module no longer ships. |
| `lola search <query>` | Search your cache by name, description and keyword. |
| `lola init [dir]` | Scaffold a module: `lola.yml`, one skill, one command. |
| `lola cache add\|rm\|list\|info\|update\|search` | Manage your cache. Bare `lola cache` lists it. |
| `lola mod add\|rm\|list\|info\|update\|search\|init` | Compatibility group (ADR #245): forwards to the `lola cache` subcommands and to `lola init`. |
| `lola version`, `lola completion <shell>`, `lola help` | The usual. |

Every command has aliases (for example `rm` for `uninstall`); `lola help` lists them.

## Sources and the cache

A **source** is a directory path or a `file://` URL to a directory. Git, archive and marketplace
sources are planned. lola copies the module into your **cache** (this build:
`$XDG_DATA_HOME/lola/modules/`, normally `~/.local/share/lola/modules/`; planned to move to
`$XDG_CACHE_HOME`) and renders from there, so a source can move or disappear without breaking
installs.

- In a git work tree, lola copies the files git would track (`.gitignore` is respected; new,
  unstaged files are included).
- `.git`, `.venv`, `node_modules`, `.lola`, `__pycache__` and `.DS_Store` are never copied.
- A directory `module/` inside the source is used as the module root. `--path <dir>` picks a
  different root, for monorepos.
- Symlinks inside a module are refused.

Once a module is cached, `lola install <name>` uses the cached copy without touching the source.
`lola cache update` fetches every cached module again; `lola list` then shows `stale` projects,
and `lola render` brings them up to date. Removing a module with `lola cache rm` does not touch
installed files, but MCP servers that run from the cache (`${PLUGIN_ROOT}`, below) stop working
until you install again.

## Module layout

```
my-module/
  lola.yml                 # optional; see below
  skills/<skill>/SKILL.md  # plus any supporting files
  SKILL.md                 # alternative: the module root is itself one skill
  commands/<command>.md    # optional commands/<command>/ directory alongside
  agents/<agent>.md        # frontmatter needs a description
  mcps.json                # {"mcpServers": {...}} in Claude's .mcp.json shape
  INSTRUCTIONS.md          # always-on instructions (project scope only)
```

`lola.yml` (only `name` is required; unknown keys are an error):

```yaml
name: my-module
description: What it does
version: 1.2.0
author: {name: You, email: you@example.com}
license: MIT
homepage: https://example.com
repository: https://example.com/repo.git
keywords: [review, git]
targets: [claude-code]          # restrict which assistants it supports
hooks: {pre-install: setup.sh}  # listed for you to review, not run in this build
```

Without `lola.yml`, the module is named after its directory. Names use `a-z`, `0-9`, `.`, `_`
and `-`, start with a letter or digit, and are at most 64 characters.

`${PLUGIN_ROOT}` (or `${CLAUDE_PLUGIN_ROOT}`) in `SKILL.md`, commands, agents and MCP server
definitions becomes the directory of the module's cached version. In this build that is inside
the cache; planned: a per-project module copy. `${PLUGIN_DATA}` becomes a per-module data
directory that lola creates and never deletes (`.lola/plugin-data/<module>` at project scope,
`$XDG_DATA_HOME/lola/plugin-data/<module>` at user scope).

**Hooks.** If a module declares install hooks, lola prints them after install and
`lola cache info` shows them. It does not run them: review the script and run it yourself if you
trust it. Planned: running them after per-module consent.

## Scopes and targets

- **Project scope** (default): files go into the project; state lives in `.lola/`. The project
  is the nearest parent directory with a `.lola/`, else the current directory; `-C <dir>` picks
  one explicitly.
- **User scope** (`--user`): files go into your home directory; state lives under
  `$XDG_CONFIG_HOME/lola/` (manifest `user.yml`) and `$XDG_STATE_HOME/lola/` (ledger
  `lock.json`). Instructions are never installed at user scope, because they would apply to every
  project; lola prints a notice instead.

When you do not pass `-t`, lola uses the manifest's targets; otherwise the assistants it detects
(`.claude/` or `CLAUDE.md` for Claude Code). On a terminal you pick from the detected ones.
`claude-code` is the only target in this build.

What lands where for `claude-code`:

| Artifact | Project scope | User scope |
|---|---|---|
| Skills | `.claude/skills/<skill>/` | `~/.claude/skills/<skill>/` |
| Commands | `.claude/commands/<command>.md` | `~/.claude/commands/` |
| Agents | `.claude/agents/<agent>.md` | `~/.claude/agents/` |
| MCP servers | `.mcp.json` → `mcpServers.<name>` | `~/.claude.json` → `mcpServers.<name>` |
| Instructions | `.claude/lola/<module>.md`, referenced from a marked block in `CLAUDE.md` | not installed |

## Files lola keeps

| File | Purpose | Commit it? |
|---|---|---|
| `.lola/project.yml` | The manifest: which modules and targets you want. | Yes |
| `.lola/lock.json` | The ledger: every file and JSON key lola wrote, with hashes. | No (gitignored for you) |
| `.lola/plugin-data/` | `${PLUGIN_DATA}` directories. | No (gitignored) |
| `.lola/.lock` | Lock held while lola changes the project. | No (gitignored) |

## Safety rules

- lola writes a file only if it is new, already identical, or one lola wrote and nobody edited.
  Anything else is a **conflict**: lola lists every conflict and changes nothing.
  `--force` overrides conflicts and lists each overridden file in the summary.
- A destination owned by another installed module is never overridden, even with `--force`.
- A JSON file that does not parse is never rewritten, even with `--force`.
- Comments and formatting in JSON files survive lola's edits; only lola's own keys change.
- lola writes through symlinked directories and shows `(via symlink → /real/path)` when a write
  leaves the project. Two destinations that resolve to the same real file are refused.
- Changes are transactional: if anything fails, every file is restored.
- `--dry-run` (`-n`) prints the plan and writes nothing.

## Output contract

- `--json`: one JSON document on stdout, snake_case keys, arrays always present (`[]` when empty).
- `--plain`: tab-separated lines, no color.
- Errors: `error: <message>` on stderr with indented `hint:` lines; under `--json`, one line
  `{"error": "...", "hints": [...]}`.
- Exit status: `0` on success or when you decline a prompt; `1` on any error.
- Color: `LOLA_COLOR=1` or `0` forces it; otherwise `NO_COLOR` disables it; otherwise it follows
  the terminal.
- Without a terminal, `uninstall` and `cache rm` need `-y`.

## Environment

| Variable | Used for |
|---|---|
| `HOME` | User-scope root; base of the XDG defaults |
| `XDG_CONFIG_HOME` | User-scope manifest (default `~/.config`) |
| `XDG_DATA_HOME` | The cache and user-scope plugin data (default `~/.local/share`) |
| `XDG_STATE_HOME` | The user-scope ledger (default `~/.local/state`) |
| `XDG_CACHE_HOME` | Download scratch space (default `~/.cache`); planned home of the cache |

Relative XDG values are ignored, as the XDG specification requires.
