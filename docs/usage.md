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
| `lola check [module-or-source...]` | Report what each assistant would drop, adapt or skip from a module, without writing anything or adding it to your cache. Defaults to the current directory. |
| `lola search <query>` | Search your cache by name, description and keyword. |
| `lola init [dir]` | Scaffold a module: `lola.yml`, one skill, one command. |
| `lola cache add\|rm\|list\|info\|update\|search` | Manage your cache. Bare `lola cache` lists it. |
| `lola version`, `lola completion <shell>`, `lola help` | The usual. |

Every command has aliases (for example `rm` for `uninstall`); `lola help` lists them.

## Sources and the cache

A **source** is where a module comes from. lola copies the module into your **cache**
(`$XDG_CACHE_HOME/lola/modules/`, normally `~/.cache/lola/modules/`), so a source can move or
disappear without breaking installs. lola recognizes these forms, first match first:

| You pass | lola reads it as |
|---|---|
| An existing directory | a folder |
| An existing file ending in `.zip`, `.tar`, `.tar.gz` or `.tgz` (any case) | an archive |
| A `file://` URL | a folder or an archive, by what the path is |
| An `https://` URL whose path ends in an archive suffix | an archive, downloaded |
| Any other `https://` or `ssh://` URL, or `user@host:path` | a git repository |
| An `http://` URL | as `https://`, but only for `localhost`, `127.0.0.0/8` or `::1` |

Anything else is not a source: a bare name means a module in your cache. Marketplace sources are
planned. lola refuses, with a hint:

- an argument, `--ref` or recorded location that starts with `-`;
- git's `<transport>::<address>` remote-helper syntax and `git://` URLs;
- credentials in a URL: any user name or password in an `http(s)` URL (GitHub and GitLab take a
  token as the user name), and any query string or fragment (presigned URLs carry secrets
  there). The URL is written to `.lola/project.yml` and your cache, where a secret would be
  committed. For git, let a credential helper or `ssh-agent` supply credentials; for a private
  archive, download it and install the file. A user name in an `ssh://` URL or in
  `git@host:path` is fine, but a password never is, and the `user:password@host:path` form is
  refused (git would pass the password to ssh as part of the remote path). `file://` URLs
  follow the same rules, and `?` or `#` in `git@host:path` is refused like a URL's query or
  fragment. No error repeats a URL's password, query or fragment, or the user name of an
  `http(s)` or `file` URL, not even for a URL that does not parse or the target of a redirect (an `ssh`
  user name is no secret and may appear);
- an existing file that is not an archive, and a local archive that is not a regular file (a
  symlink, pipe or device named like an archive).

Adding a module to the cache normalizes it: the cached copy holds exactly one `lola.yml`,
generated from the module's metadata. lola reads the first of `lola.yaml`, `lola.yml`,
`.lola.yaml` and `.lola.yml`, and warns about each one it ignores.

For every kind of source:

- `.git`, `.venv`, `node_modules`, `.lola`, `__pycache__` and `.DS_Store` are never copied.
- Symlinks inside a module are refused.
- `--path <dir>` picks the module root inside the source, for monorepos.

**Folders.** In a git work tree, lola copies the files git would track (`.gitignore` is
respected; new, unstaged files are included). A directory `module/` inside the source is the
module root unless you pass `--path`. `--ref` is refused.

**Git.** lola runs your `git` (it must be on `PATH`) with a shallow clone of the default
branch, or of `--ref`: a branch name, a tag name, or a full 40- or 64-character commit SHA.
lola never guesses: a ref that names both a branch and a tag is an error, an abbreviated SHA
is an error, and a hex-looking branch name is a branch. A commit that no branch or tag points
to needs a server that serves such commits (servers speaking git protocol v2 do). Only committed
files are copied; the module root is `--path`, else `module/`, else the repository root. lola
records the commit it fetched.

- Your git config, credential helpers and `ssh-agent` are used. Git hooks, `core.fsmonitor`,
  the `ext::` transport and Git LFS filters are switched off for the repository lola fetches.
- **Git LFS files are not fetched.** A module receives LFS pointer files as they are committed.
- git never prompts for a user name or password (`GIT_TERMINAL_PROMPT=0`). ssh still can: it
  asks on the terminal for a key passphrase or an unknown host key. Load your key into
  `ssh-agent` and add the host to `known_hosts` first, or the fetch waits until it gives up
  after 5 minutes. A child process that keeps git's output open (ssh prompting, a stalled
  https helper) delays that stop by at most 5 more seconds.

**Archives.** zip, tar and gzip tar, chosen by the file name's suffix. `--ref` is refused.
The module root is the first of:

1. `--path`, relative to the archive's root as `tar tf` or `unzip -l` show it;
2. the shallowest directory holding `lola.yaml`, `lola.yml`, `.lola.yaml`, `.lola.yml`,
   `plugin.json` or `.claude-plugin/`;
3. the parent of the shallowest `skills/`, `commands/`, `agents/` or `rules/` directory;
4. the archive's single top-level directory, if that is all it holds;
5. the archive's root.

Two different candidates at the same depth are an error; pick one with `--path`. Without a
`lola.yml` name, the module is named after its root directory, or after the archive file
(without its suffix) when the root is the archive's root.

An archive may hold only regular files and directories. lola refuses the whole archive if any
entry is absolute, contains `..` or a backslash, is a link or device, or repeats a path. Files
keep only their execute bit (`0755` or `0644`). Limits, which are not configurable:

| Limit | Value |
|---|---|
| Entries | 20,000 |
| Zip central directory | about 19.5 MiB (1 KiB for each of 20,000 entries), checked before it is read |
| Unpacked size | 512 MiB in total |
| Compression ratio of one zip entry | 200 to 1 |
| Download size | 256 MiB |
| Download time | 2 minutes |
| Redirects | 5, never from `https` to `http`, and never to `http` off loopback |

Once a module is cached, `lola install <name>` uses the cached copy without touching the source.
`lola cache update` fetches every cached module again: a git branch (or the default branch) may
have moved, a local archive is read again, and an archive URL is downloaded again. The content
digest decides whether anything changed; `lola list` then shows `stale` projects, and
`lola render` brings them up to date.

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
session. lola delivers rules where the assistant loads them natively (see the tables below) and
never edits your `CLAUDE.md`, `GEMINI.md` or `AGENTS.md`. Gemini CLI reads them only in folders
you trust, and project rules only when it starts in the project root.
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
- **User scope** (`--user`): files go into your home directory (OpenCode's into
  `$XDG_CONFIG_HOME/opencode/`); state lives under `$XDG_CONFIG_HOME/lola/` (manifest
  `user.yml`) and `$XDG_STATE_HOME/lola/` (ledger `lock.json`). Rules installed at user scope
  apply in every project.

The targets are `claude-code` (Claude Code), `gemini-cli` (Gemini CLI) and `opencode`
(OpenCode).

When you do not pass `-t`, lola uses the manifest's targets. Otherwise it offers the assistants
installed on your machine (their command, `claude`, `gemini` or `opencode`, is on your
`PATH`): on a terminal you pick from them, with those already set up in the project preselected,
or every installed assistant preselected when none is set up; off a terminal lola uses the ones
both installed and set up. An assistant is set up in a project when it has `.claude/` or
`CLAUDE.md` (Claude Code), `.gemini/` or `GEMINI.md` (Gemini CLI), or `.opencode/`,
`opencode.json` or `opencode.jsonc` (OpenCode); at user scope, when `~/.claude/` or
`~/CLAUDE.md`, `~/.gemini/` or `~/GEMINI.md`, or `$XDG_CONFIG_HOME/opencode/` exists. A target
you name with `-t`, or that the manifest lists, is always rendered, installed or not, for
example in a container that only prepares files.

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

For `gemini-cli` (user scope: the same paths under `~`):

| Artifact | Project scope |
|---|---|
| Skills | `.gemini/skills/<skill>/`; `name` is added from the directory when missing; Gemini neither runs shell nor fills argument placeholders in skills, so lola warns when a skill has them |
| Commands | `.gemini/commands/<command>.toml`: `description` and `prompt`, with `$ARGUMENTS` as `{{args}}`; Gemini has no single-argument placeholders, so `$ARGUMENTS[N]`, `$N` and the names listed in `arguments:` stay as written and the prompt starts with the whole argument string; Gemini has no escape, so a Claude-escaped `\$1` becomes `$1` (only a single backslash escapes; after `\\` the placeholder is still read as one); a `commands/<command>/` directory is copied alongside, and Gemini reads every `*.toml` file inside it as a namespaced command (`<command>:<file>`) |
| Agents | `.gemini/agents/<agent>.md`, keeping only `name`, `description`, `kind` (`local` or `remote`), `display_name`, `tools` (a list of Gemini tool names such as `read_file` or `mcp_<server>_<tool>`), `model` (`gemini-…`), `temperature`, `max_turns` and `timeout_mins` (positive integers). Gemini refuses to load an agent with any other key or value of another shape, so lola drops those instead. An agent whose `tools` uses other names, such as Claude's `Read` or `mcp__server__tool`, is skipped: Gemini gives an agent without `tools` every tool, so dropping the list would lift the restriction (`--force` installs it without the list) |
| MCP servers | `.gemini/settings.json` → `mcpServers.<name>`: `command`, `args`, `env`, `cwd`; `httpUrl` for http, `url` for sse |
| Rules | `.gemini/lola/<module>/GEMINI.md` (the module's rules in name order), listed in `.gemini/settings.json` → `context.includeDirectories` (at user scope by absolute path, so every project's Gemini session can read that directory). Gemini loads these rules, and project MCP servers, only in a trusted folder, and project-scope ones only when `gemini` starts at the project root, not in a subdirectory |

For `opencode` (user scope: the same paths under `$XDG_CONFIG_HOME/opencode/`, without
`.opencode/`):

| Artifact | Project scope |
|---|---|
| Skills | `.opencode/skills/<skill>/`; names are lowercase words joined by single hyphens, and a `name` in the frontmatter must equal the directory; OpenCode neither runs shell nor fills argument placeholders in skills, so lola warns when a skill has them |
| Commands | `.opencode/commands/<command>.md`, keeping only `description`, `agent`, `model` (`provider/model`) and `subtask`; Claude counts arguments from 0 and OpenCode from 1, so `$0`, `$ARGUMENTS[0]` and the first name listed in `arguments:` become `$1`, and so on (`$ARGUMENTS` stays); OpenCode has no escape, so a Claude-escaped `\$1` or `\$ARGUMENTS` is kept but OpenCode still fills it in; OpenCode runs ``!`…` `` anywhere in a command without Claude's permission checks and never runs a ```` ```! ```` fence; a `commands/<command>/` directory is not copied, as OpenCode would read its files as commands |
| Agents | `.opencode/agents/<agent>.md` with `mode: subagent` unless the agent sets a mode; `name` is dropped and `model` is kept only as `provider/model`. OpenCode cannot express a tool list and gives an agent without one every tool, so an agent with a non-empty `tools` is skipped (`--force` installs it without the list). OpenCode refuses to start on an invalid agent value, so lola drops a `color` other than `#rrggbb` (OpenCode 1.18 also takes theme names such as `accent`, but later versions reject them), a `mode` other than `subagent`, `primary` or `all`, a non-numeric `temperature` or `top_p`, a `steps` or `maxSteps` that is not a positive integer, a non-boolean `hidden` or `disable`, a non-string `variant` and a non-map `options`. `permission` is passed through unchecked |
| MCP servers | `opencode.jsonc` if it exists, else `opencode.json` → `mcp.<name>`: `{"type": "local", "command": [...], "environment"}` or `{"type": "remote", "url", "headers"}`; `${VAR}` becomes `{env:VAR}`. OpenCode merges both files and `opencode.jsonc` wins, so a server of the same name in `opencode.json` is overridden by lola's |
| Rules | `.opencode/lola/<module>/<rule>.md`, listed as `.opencode/lola/<module>/*.md` in the same file's `instructions` (at user scope by absolute path) |

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
| `name-rejected` | skip | yes | The target rejects the artifact's name (for `opencode`, also a skill whose `name` differs from its directory). |
| `required-key-missing` | skip | yes | A frontmatter key the target needs is absent, empty, or was dropped for having the wrong type. |
| `agent-tools-unenforceable` | skip | yes | The agent restricts its tools in a way the target cannot enforce (`gemini-cli`: names that are not Gemini's; `opencode`: any non-blank `tools` value, list or string). The assistant would give the agent every tool, so it is left out unless `--force`, which installs it without the list. |
| `frontmatter-invalid` | skip | no | The frontmatter parses but lola cannot re-encode it. |
| `frontmatter-key-dropped` | warn or info | no | The target does not support a frontmatter key, so it is left out. |
| `frontmatter-value-dropped` | warn | no | A frontmatter key has a value of the wrong type or form for the target (for example an OpenCode agent `color: red`, or a Gemini agent `max_turns: 1.5`), so it is left out. |
| `frontmatter/yaml-ambiguous` | warn | no | An unquoted frontmatter value that YAML parsers read differently; quote it. |
| `version/unsupported` | warn | no | Your installed assistant is older than the version lola renders for and ignores something the module uses. |
| `version/limited` | warn | no | Your installed assistant has a known limitation that affects this module. |
| `module-warning` | warn | no | The module itself has a problem, such as a legacy instruction file or a skipped rule entry. |
| `mcp-cwd-dropped` | warn | no | The target has no working-directory setting for an MCP server, so `cwd` is dropped. |
| `command-sidecar-dropped` | warn | no | `opencode` would read a command's `commands/<command>/` files as commands, so the directory is not copied. |
| `gemini-cli/shell-injection` | skip | yes | A command runs shell with Claude's ``!`…` `` (at a line start or after whitespace) or a ```` ```! ```` fenced block, which Gemini CLI would send to the model as text; Gemini's own syntax is `!{…}`. |
| `gemini-cli/arguments` | info | no | `$ARGUMENTS` becomes Gemini's `{{args}}`. |
| `gemini-cli/positional-arguments` | warn | no | Gemini CLI has no positional or named arguments: `$ARGUMENTS[N]`, `$N` and names declared in `arguments:` stay as written and the prompt starts with the whole argument string. |
| `opencode/arguments-renumbered` | warn | no | Claude's 0-based `$N`, `$ARGUMENTS[N]` and declared names became OpenCode's 1-based `$1`, `$2`, …; check the command with a few arguments, as OpenCode may split them differently. |
| `opencode/shell-unchecked` | warn | no | A command has ``!`…` ``, which OpenCode runs wherever it appears, without Claude's permission checks (`allowed-tools` is dropped); review the command before installing. |
| `opencode/shell-fence` | skip | yes | A command runs shell in a ```` ```! ```` fenced block, which OpenCode does not run and would send to the model as text; use inline ``!`…` ``. |
| `skill-shell-dropped` | warn | no | A skill runs shell with ``!`…` `` or a ```` ```! ```` block; `gemini-cli` and `opencode` do not run shell in skills, so it reaches the model as text. |
| `skill-arguments-dropped` | warn | no | A skill uses `$ARGUMENTS`, `$ARGUMENTS[N]` or `$N`; `gemini-cli` and `opencode` do not fill argument placeholders in skills, so they reach the model as written. |
| `opencode/escaped-arguments` | warn | no | A command has a Claude-escaped `\$N` or `\$ARGUMENTS`; OpenCode has no escape and still replaces it with arguments, so reword the text. |
| `opencode/env-reference` | info | no | `${VAR}` in an MCP server becomes OpenCode's `{env:VAR}`. |
| `opencode/env-default-dropped` | warn | no | OpenCode has no defaults for environment references: `${VAR:-default}` becomes `{env:VAR}`. |
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
- lola refuses a project whose `.lola/`, `.lola/modules/` or `.lola/plugin-data/` is a symlink,
  and replaces a module copy that is not a real directory.
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
| `XDG_CONFIG_HOME` | User-scope manifest, and OpenCode's user-scope directory (default `~/.config`) |
| `XDG_DATA_HOME` | User-scope module copies and plugin data (default `~/.local/share`) |
| `XDG_STATE_HOME` | The user-scope ledger, hook consent and lock files (default `~/.local/state`) |
| `XDG_CACHE_HOME` | The cache, download scratch space and assistant version cache (default `~/.cache`) |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Archive downloads (git reads them itself) |
| `GIT_*`, `SSH_AUTH_SOCK` | Passed to git, except the variables that pick a repository or add config behind lola's back (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_CONFIG_PARAMETERS`, `GIT_CONFIG_COUNT`, …) |

Relative XDG values are ignored, as the XDG specification requires.
