# Using lola

`lola help` is the complete, always-current reference (piped, it prints every command with its
aliases and flags). This page explains the concepts behind it. It describes this build; planned
changes are marked "planned" and tracked in [roadmap.md](roadmap.md).

## Commands

| Command | What it does |
|---|---|
| `lola install <source\|name>...` | Copy the module into your cache (if it is a source), render it for each target, record it in the manifest. |
| `lola uninstall <module>...` | Remove everything lola wrote for the module. Asks first; `-y` skips the question. |
| `lola list` | Each installed module × target with a status: `ok`, `modified` (a file lola wrote was edited), `missing` (a file lola wrote, or the module copy, is gone), `stale` (your cache has a newer version), or `skipped` (nothing of the module could be rendered for that target); and whether the module's rules are on. |
| `lola render` | Render every installed module again from your cache. Removes files a module no longer ships. |
| `lola check [module-or-folder...]` | Report what each assistant would drop, adapt or skip from a module, without writing anything or adding it to your cache. Defaults to the current directory. |
| `lola search <query>` | Search your cache by name, description and keyword. |
| `lola init [dir]` | Scaffold a module: `lola.yml`, one skill, one command. |
| `lola cache add\|rm\|list\|info\|update\|search` | Manage your cache. Bare `lola cache` lists it. |
| `lola version`, `lola completion <shell>`, `lola help` | The usual. |

Every command has aliases (for example `rm` for `uninstall`); `lola help` lists them.

## Sources and the cache

A **source** is a directory path or a `file://` URL to a directory. Git, archive and marketplace
sources are planned. lola copies the module into your **cache**
(`$XDG_CACHE_HOME/lola/modules/`, normally `~/.cache/lola/modules/`), so a source can move or
disappear without breaking installs.

Adding a module to the cache normalizes it: the cached copy holds exactly one `lola.yml`,
generated from the module's metadata. lola reads the first of `lola.yaml`, `lola.yml`,
`.lola.yaml` and `.lola.yml`, and warns about each one it ignores.

- In a git work tree, lola copies the files git would track (`.gitignore` is respected; new,
  unstaged files are included).
- `.git`, `.venv`, `node_modules`, `.lola`, `__pycache__` and `.DS_Store` are never copied.
- A directory `module/` inside the source is used as the module root. `--path <dir>` picks a
  different root, for monorepos.
- Symlinks inside a module are refused.

Once a module is cached, `lola install <name>` uses the cached copy without touching the source.
`lola cache update` fetches every cached module again; `lola list` then shows `stale` projects,
and `lola render` brings them up to date.

Installing a module also gives it a **module copy**: `.lola/modules/<module>/` in the project,
or `$XDG_DATA_HOME/lola/user-modules/<module>/` at user scope. `${PLUGIN_ROOT}` (below) points
there, so removing a module with `lola cache rm` never breaks an installed one; `lola render`
fetches the source again if it needs to. Hooks may build inside the copy: lola leaves its content
alone, replaces the copy only for a new version or when it is gone, and then runs the hooks again.

## Module layout

```
my-module/
  lola.yml                 # optional; see below
  skills/<skill>/SKILL.md  # plus any supporting files
  SKILL.md                 # alternative: the module root is itself one skill
  commands/<command>.md    # optional commands/<command>/ directory alongside
  agents/<agent>.md        # frontmatter needs a description
  mcps.json                # {"mcpServers": {...}} in Claude's .mcp.json shape
  rules/<rule>.md          # always-on rules (below)
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
hooks: {pre-install: setup.sh}  # run only with your consent (below)
```

Without `lola.yml`, the module is named after its directory. Names use `a-z`, `0-9`, `.`, `_`
and `-`, start with a letter or digit, and are at most 64 characters.

`${PLUGIN_ROOT}` (or `${CLAUDE_PLUGIN_ROOT}`) in `SKILL.md`, commands, agents and MCP server
definitions becomes the module copy's directory. `${PLUGIN_DATA}` becomes a per-module data
directory that lola creates and never deletes (`.lola/plugin-data/<module>` at project scope,
`$XDG_DATA_HOME/lola/plugin-data/<module>` at user scope).

**Rules.** Each `rules/<rule>.md` is always-on context: your assistant loads it in every
session. lola delivers rules where the assistant loads them natively (for Claude Code,
`.claude/rules/lola/<module>/`) and never edits your `CLAUDE.md`, `GEMINI.md` or `AGENTS.md`.
Frontmatter is removed; a `paths:` key is an error, because guidance for some files belongs in a
skill. The rest of a rule is delivered verbatim: `${PLUGIN_ROOT}` is not expanded in rules. A
module-root `INSTRUCTIONS.md` or `AGENTS.md` is not installed; lola warns and suggests moving it
to `rules/`. `lola install --no-rules` installs everything else and records `rules: false` on
the module's manifest entry, so later renders keep it off; `--rules` turns them back on.

**Hooks.** `pre-install` and `post-install` name executable scripts inside the module. They run
as you, unconfined, once per target, in the module copy, with `LOLA_MODULE_NAME`,
`LOLA_MODULE_PATH`, `LOLA_PROJECT_PATH`, `LOLA_ASSISTANT`, `LOLA_SCOPE` and `LOLA_HOOK` set; their
output goes to stderr, and each run is stopped after 10 minutes. lola runs them only with your
consent: on a terminal it shows each script with its sha256 and asks (default No), and
remembers a yes until a hook script changes. Consent covers the hook scripts themselves, not
other files they run, so review those too. `--allow-hooks` consents for one run; `-y` never does.
Without consent a `pre-install` hook stops the install before anything is written, and a
`post-install` hook is skipped with a warning. A failing `pre-install` hook installs nothing; a
failing `post-install` hook leaves the installation in place with a warning. `lola render` runs
hooks only for modules whose version changed, and `--dry-run` lists hooks without running them.

## Scopes and targets

- **Project scope** (default): files go into the project; state lives in `.lola/`. The project
  is the nearest parent directory with a `.lola/`, else the current directory; `-C <dir>` picks
  one explicitly.
- **User scope** (`--user`): files go into your home directory; state lives under
  `$XDG_CONFIG_HOME/lola/` (manifest `user.yml`) and `$XDG_STATE_HOME/lola/` (ledger
  `lock.json`). Rules installed at user scope apply in every project.

When you do not pass `-t`, lola uses the manifest's targets. Otherwise it offers the assistants
installed on your machine (their command, such as `claude`, is on your `PATH`): on a terminal
you pick from them, with those already set up in the project preselected (`.claude/` or
`CLAUDE.md` for Claude Code), or every installed assistant preselected when none is set up; off a terminal lola uses the ones both installed and set up. A
target you name with `-t`, or that the manifest lists, is always rendered, installed or not,
for example in a container that only prepares files. `claude-code` is the only target in this
build.

lola runs an installed assistant once, with `--version`, to learn which version you have, and
caches the answer in `$XDG_CACHE_HOME/lola/probes.json` until the binary changes. The version
never changes what lola writes (teammates on other versions get the same files); it only adds
warnings when your version lacks something the module uses.

What lands where for `claude-code`:

| Artifact | Project scope | User scope |
|---|---|---|
| Skills | `.claude/skills/<skill>/` | `~/.claude/skills/<skill>/` |
| Commands | `.claude/commands/<command>.md` | `~/.claude/commands/` |
| Agents | `.claude/agents/<agent>.md` | `~/.claude/agents/` |
| MCP servers | `.mcp.json` → `mcpServers.<name>` | `~/.claude.json` → `mcpServers.<name>` |
| Rules | `.claude/rules/lola/<module>/<rule>.md` | `~/.claude/rules/lola/<module>/<rule>.md` |

## Findings

When a target cannot express part of a module, lola tells you instead of failing the whole
module. Each **finding** has a severity:

- **info**: a lossless adaptation. Shown by `lola check` and in `--json` output.
- **warn**: something is dropped or changed, but the artifact works; it is installed.
- **skip**: that artifact is left out for that target, because the assistant could not use it
  (for example an agent without a `description`); everything else is installed.

`--force` also renders skipped artifacts that can be rendered (it never invents a place for
something the assistant has no place for). `--strict` turns every remaining warn or skip into an
error before anything is written; use it in CI. A `--force` render is not remembered: a later
`lola render` without `--force` skips (and removes) those artifacts again.

Each finding has a stable code, shown by `--plain` and `--json`. A forceable skip is one that
`--force` can render; a skip that `--force` rendered is reported as a warn.

| Code | Severity | Forceable | Meaning |
|---|---|---|---|
| `kind-unsupported` | skip | no | The target has no place for this kind of artifact. |
| `name-rejected` | skip | yes | The target rejects the artifact's name. |
| `required-key-missing` | skip | yes | A frontmatter key the target needs is absent, empty, or was dropped for having the wrong type. |
| `frontmatter-invalid` | skip | no | The frontmatter parses but lola cannot re-encode it. |
| `frontmatter-key-dropped` | warn | no | The target does not support a frontmatter key, so it is left out. |
| `frontmatter-value-dropped` | warn | no | A frontmatter key has a value of the wrong type for the target, so it is left out. |
| `frontmatter/yaml-ambiguous` | warn | no | An unquoted frontmatter value that YAML parsers read differently; quote it. |
| `version/unsupported` | warn | no | Your installed assistant is older than the version lola renders for and ignores something the module uses. |
| `version/limited` | warn | no | Your installed assistant has a known limitation that affects this module. |
| `module-warning` | warn | no | The module itself has a problem, such as a legacy instruction file or a skipped rule entry. |
| `mcp-cwd-dropped` | warn | no | The target has no working-directory setting for an MCP server, so `cwd` is dropped. |
| `probe/not-found` | info | no | The assistant is not on `PATH`; lola checked against the newest version it knows. |
| `probe/unknown-version` | info | no | The assistant's version could not be read. |
| `probe/cache` | info | no | The version was read but could not be cached. |

lola only reports what its own rendering loses. For general advice on writing skills, commands
and agents, use a dedicated linter such as [skillsaw](https://github.com/stbenjam/skillsaw).

## Files lola keeps

| File | Purpose | Commit it? |
|---|---|---|
| `.lola/project.yml` | The manifest: which modules and targets you want. | Yes |
| `.lola/lock.json` | The ledger: every file and JSON key lola wrote, with hashes. | No (gitignored for you) |
| `.lola/modules/` | Module copies that `${PLUGIN_ROOT}` points into. | No (gitignored) |
| `.lola/plugin-data/` | `${PLUGIN_DATA}` directories. | No (gitignored) |

lola owns `.lola/.gitignore` and rewrites it if it changes. The lock lola holds while it
changes a project lives outside the project, in `$XDG_STATE_HOME/lola/locks/`.

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
| `PATH` | Where lola looks for installed assistants; relative entries are ignored |
| `XDG_CONFIG_HOME` | User-scope manifest (default `~/.config`) |
| `XDG_DATA_HOME` | User-scope module copies and plugin data (default `~/.local/share`) |
| `XDG_STATE_HOME` | The user-scope ledger, hook consent and lock files (default `~/.local/state`) |
| `XDG_CACHE_HOME` | The cache, download scratch space and assistant version cache (default `~/.cache`) |

Relative XDG values are ignored, as the XDG specification requires.
