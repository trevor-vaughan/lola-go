# Using lola

`lola help` is the complete, always-current reference (piped, it prints every command with its
aliases and flags). This page explains the concepts behind it. It describes this build; planned
changes are marked "planned" and tracked in [roadmap.md](roadmap.md).

## Commands

| Command | What it does |
|---|---|
| `lola install [<source\|name>...]` | Copy the module into your cache (if it is a source), render it for each target, record it in the manifest. With no argument on a terminal, walks you through the choices ([Choosing what to install](#choosing-what-to-install)). |
| `lola uninstall <module>...` | Remove everything lola wrote for the module. Asks first; `-y` skips the question. |
| `lola list` | Each installed module × target with a status: `ok`, `modified` (a file lola wrote was edited), `missing` (a file lola wrote, or the module copy, is gone), `stale` (your cache has a newer version), or `skipped` (nothing of the module could be rendered for that target); whether the module's rules are on; and, when any module has some, how many of its components `selections.yaml` skips. |
| `lola render` | Render every installed module again from your cache. Removes files a module no longer ships. |
| `lola check [module-or-source...]` | Report what each assistant would drop, adapt or skip from a module, without writing anything or adding it to your cache. Defaults to the current directory. |
| `lola search <query>` | Search your cache by name, description and keyword. |
| `lola init [dir] [--format agent-plugins\|lola]` | Scaffold a module named after the directory: by default an Agent Plugins package (`plugin.json`, one skill, one `dev.getlola` command); `--format lola` writes `lola.yml`, one skill and one command. It never overwrites a file and refuses a directory holding a module or a symbolic link on the way to a file it would write. |
| `lola cache add\|rm\|list\|info\|update\|search` | Manage your cache. Bare `lola cache` lists it. |
| `lola migrate` | Move from the Python lola: install what it installed, with lola, and clean up after it ([Coming from the Python lola](#coming-from-the-python-lola)). |
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
generated from the module's manifest. lola reads the first manifest it finds (see
[Module formats](#module-formats)) and warns about each lower-precedence one it ignores. A
plugin format is converted once, here: the generated `lola.yml` lists every artifact the plugin
declared, so a cached version means the same thing to every later lola.

For every kind of source:

- `.git`, `.venv`, `node_modules`, `.lola`, `__pycache__` and `.DS_Store` are never copied.
- Symlinks inside a module are refused.
- `--path <dir>` picks the module root inside the source, for monorepos.

**Folders.** In a git work tree, lola copies the files git would track (`.gitignore` is
respected; new, unstaged files are included). A directory `module/` inside the source is the
module root unless you pass `--path`; `--path .` picks the folder itself. `--ref` is refused.

**Git.** lola runs your `git` (it must be on `PATH`) with a shallow clone of the default
branch, or of `--ref`: a branch name, a tag name, or a full 40- or 64-character commit SHA.
lola never guesses: a ref that names both a branch and a tag is an error, an abbreviated SHA
is an error, and a hex-looking branch name is a branch. A commit that no branch or tag points
to needs a server that serves such commits (servers speaking git protocol v2 do). Only committed
files are copied; the module root is `--path`, else `module/`, else the repository root.
`--path .` picks the repository root and names the module after the repository. lola records
the commit it fetched.

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
definitions becomes the module copy's directory. `${PLUGIN_DATA}` (or `${CLAUDE_PLUGIN_DATA}`)
becomes a per-module data directory that lola creates and never deletes
(`.lola/plugin-data/<module>` at project scope, `$XDG_DATA_HOME/lola/plugin-data/<module>` at
user scope).

**Frontmatter.** lola reads the frontmatter of every Markdown component (skills, commands,
agents and rules), and refuses what another parser might read differently, because that could
hide a key, hooks included, from what lola checks and shows. It refuses a file when:

- its first line, after an optional byte-order mark, starts with `---` but is not exactly
  `---` (`----`, `---` with trailing spaces or text, or with no line break). Hint: "start
  frontmatter with a line of exactly ---; for a rule (horizontal line) at the top, write *** or
  ___ instead, or begin the file with other text".
- its frontmatter holds a second YAML document. Hint: "remove the extra document, or quote the
  line that looks like a YAML document separator".
- its frontmatter has a `<<` merge key at any depth (a quoted `'<<'` is an ordinary key). Hint:
  "write the merged keys out in the frontmatter".

When lola writes a component that has no frontmatter and a body whose first line starts with
`---`, it puts an empty frontmatter block (`---` twice) before the body, so no assistant reads
the body as frontmatter.

**Rules.** Each `rules/<rule>.md` is always-on context: your assistant loads it in every
session. lola delivers rules where the assistant loads them natively (see the tables below) and
never edits your `CLAUDE.md`, `GEMINI.md` or `AGENTS.md`. Gemini CLI reads them only in folders
you trust, and project rules only when it starts in the project root.
Frontmatter is removed; a `paths:` key is an error, because guidance for some files belongs in a
skill. The rest of a rule is delivered verbatim: `${PLUGIN_ROOT}` is not expanded in rules. A
rule whose remaining text starts with `---` gets an empty `---` block first, so that text is
never read as the rule's frontmatter. A
module-root `INSTRUCTIONS.md` or `AGENTS.md` is not installed; lola warns and suggests moving it
to `rules/`. `lola install --no-rules` installs everything else and records `rules: false` on
the module's manifest entry, so later renders keep it off; `--rules` turns them back on.

### Module formats

lola reads a module from the first of these it finds; the others are ignored, with a warning:

1. `lola.yaml`, `lola.yml`, `.lola.yaml` or `.lola.yml` (in that order);
2. a root `plugin.json` whose `$schema` is
   `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json`: an
   [Agent Plugins](https://agent-plugins.org/specification) package. A `plugin.json` with any
   other `$schema` is not one: lola warns and keeps looking;
3. `.claude-plugin/plugin.json`: a Claude Code plugin;
4. the native layout above.

Formats are detected by `cache add`, `install` of a source and `check` of a source. The cached
copy keeps the plugin's own files where they are and gains a `lola.yml` with `format:` and a
`components:` section (below) listing every artifact the plugin declared, so loading it never
interprets the plugin format again. Before storing a version, lola reloads that copy as
an install will and refuses the module if it reads differently from the source ("could not
cache ... faithfully"); that is a lola bug to report. Warnings about the plugin (unsupported
keys, skipped skills) are shown when the module is added or checked from its source; they are
not stored, so later installs from your cache do not repeat them.

**Claude Code plugins** (`.claude-plugin/plugin.json`):

- `name` (required), `version`, `description`, `author`, `homepage`, `repository`, `license`
  and `keywords` become the module's metadata. The name must follow lola's name rule.
- Component paths start with `./` (`skills` also takes `.`), stay inside the plugin and exist;
  `hooks` and `mcpServers` paths end in `.json`. A `null` component value is an error.
- `skills` add to `skills/`; every listed directory is one skill if it holds `SKILL.md`,
  otherwise each `<name>/SKILL.md` below it is. `commands` and `agents` replace `commands/` and
  `agents/` (lola warns when the default directory is then ignored); directories of commands
  are read recursively, and the `commands` object map becomes inline commands (`argumentHint`
  and `allowedTools` become the `argument-hint` and `allowed-tools` frontmatter keys; lola
  warns that `commands/` is then ignored). A default directory that is a symbolic link is
  skipped with a warning. An MCP server field lola does not read warns.
  `mcpServers` and `hooks` merge with `.mcp.json` and `hooks/hooks.json`.
- lola has no namespaces, so two commands, agents or skills with one name (for example
  `commands/a/build.md` and `commands/b/build.md`) are an error, and so is an MCP server
  declared twice, where Claude Code would let the later one replace the earlier one. A plugin
  that declares nothing lola can install is an error ("declares nothing lola can install"), as
  is an empty hook set.
- `${CLAUDE_PLUGIN_ROOT}` and `${CLAUDE_PLUGIN_DATA}` mean `${PLUGIN_ROOT}` and
  `${PLUGIN_DATA}`; `${CLAUDE_PROJECT_DIR}` is left as written.
- `lspServers`, `outputStyles`, `workflows`, `experimental`, `settings`, `userConfig`,
  `channels`, `dependencies`, `types`, MCP bundles (`.mcpb`, `.dxt`) and keys lola does not know
  are not installed: lola warns and keeps them under `formats.claude-plugin`.
- Hooks are Claude Code runtime hooks. lola installs them into Claude Code only, and only with
  your consent (see [Runtime hooks](#runtime-hooks) below); the other assistants report them
  as `kind-unsupported`.

**Agent Plugins packages** (root `plugin.json`):

- The manifest takes only `$schema`, `name`, `version`, `description`, `author`, `homepage`,
  `repository`, `license`, `keywords` and `extensions`; anything else is an error. `name` uses
  `a-z`, `0-9`, `.` and `-`, starts and ends with a letter or digit, has no `--` or `..`, and is
  at most 64 characters.
- Each `skills/<name>/SKILL.md` is a skill. One that breaks the
  [Agent Skills](https://agentskills.io/specification) rules (a `name` equal to its directory,
  a `description` of 1 to 1024 characters, a `compatibility` of at most 500; limits count
  characters) is skipped with a warning, as is a `skills` that is not a directory. A package
  that declares nothing lola can install is an error ("declares nothing lola can install").
- `mcp.json` is checked strictly; any problem is an error: exactly `$schema` and `mcpServers`;
  types `stdio`, `streamable-http` and `sse`; a `command` that is one bare name or a `./` path, without `..` or control characters;
  no `PLUGIN_ROOT` or `PLUGIN_DATA` in `env`; a `cwd` under `./`, `${PLUGIN_ROOT}` or
  `${PLUGIN_DATA}` that stays inside it; `http(s)` URLs without user information or a fragment,
  `https` unless the host is loopback; no placeholders in `command`, `url` or headers, and no
  control characters in header values other than tab; `null` where the schema wants a type. lola
  gives every stdio server `PLUGIN_ROOT` and `PLUGIN_DATA` in its environment. Any other
  `${NAME}` in a stdio server's `args`, `env` or `cwd` is kept, with a warning: Agent Plugins
  treats it as literal text, but your assistants expand it.
- Client namespaces reach only their own assistant: `com.anthropic.claude` and
  `com.anthropic.claude-code` reach `claude-code`, `ai.opencode` reaches `opencode`,
  `com.google.gemini-cli` reaches `gemini-cli`, and `dev.getlola` reaches all of them. Each
  namespace's commands and agents are the `*.md` files in `extensions."<ns>".commands` and
  `.agents` (`./` directories, by default `<ns>/commands/` and `<ns>/agents/`).
  `extensions."<ns>".instructions` names a file delivered as the module's rule; a namespace that
  does not declare it delivers none.
- When `dev.getlola` and a client namespace provide a command, agent or rule with the same name,
  `dev.getlola`'s wins, with a warning. The two Anthropic namespaces providing the same name is
  an error. Other namespaces are ignored with a warning, and their contents are not read; so
  are keys lola does not read inside a namespace.
- Nothing is installed into `.agents/plugins/`: lola renders the package into your assistants
  like any module.

### `components`

`lola.yml` may list a module's artifacts explicitly. Format adapters write this section; you
can write it too.

```yaml
name: review
components:
  skills:
    - {path: skills/review}                 # one directory holding SKILL.md
  commands:
    - {path: extra/deploy.md}
    - {path: opencode/fix.md, targets: [opencode]}
    - name: status                          # frontmatter overrides or adds keys
      path: commands/status.md
      frontmatter: {argument-hint: "[env]"}
    - {name: about, content: "Explain what this module provides."}
  agents:
    - {path: agents/reviewer.md}
  mcp:
    - {path: .mcp.json}                     # {"mcpServers": {...}}
    - {name: github, server: {type: stdio, command: gh-mcp}}
  rules:
    - {path: docs/STYLE.md, name: style}
  runtime_hooks:
    - {path: hooks/hooks.json}              # {"hooks": {...}}
```

- **A listed kind replaces discovery.** Listing `commands` (even as `[]`) means `commands/` is
  not read; kinds you do not list are still discovered from the layout above.
- **Paths** are relative to the module root, stay inside it, exist, and go through no symbolic
  link. A skill entry names one skill directory; command, agent and rule entries name one `.md`
  file.
- **Names** come from `name:`, else the path: the directory for a skill, the file name without
  `.md` otherwise.
- **`targets`** restricts an entry to some assistants, within the module's own `targets`.
- **Duplicates.** Two artifacts of one kind with the same name are an error unless their
  `targets` keep them apart.
- `format:` records which adapter wrote the file (`claude-plugin` or `agent-plugins`); leave it
  out of a `lola.yml` you write. `formats:` holds anything, and lola never reads it.
- Every JSON file lola reads from a module (`mcps.json`, `plugin.json`, `.mcp.json`,
  `mcp.json`, hooks files) is a regular file, not a symbolic link, of at most 1 MiB. No object
  may repeat a key, or hold two keys that differ only in case (such as `command` and
  `Command`), except the names in a server's `env` or `headers` (exactly
  `/mcpServers/<server>/env` and `/mcpServers/<server>/headers`, or
  `/mcpServers/<i>/<server>/env` and `/headers` in a Claude plugin's list form), where
  `HTTP_PROXY` and `http_proxy` may both appear.
- A hooks event lists at least one matcher group: `{"Stop": []}` is an error. Each group has an
  optional string `matcher` and a non-empty `hooks` list of handlers; each handler has a `type`,
  and the fields lola rewrites or shows for consent must have Claude Code's types (`command`,
  `args`, `shell`, `if`, `async`, `url`, `headers`, `allowedEnvVars`, `server`, `tool`,
  `prompt`). Other fields, and handler types lola does not know, are kept as written.
- An MCP server in Claude's `.mcp.json` shape (`mcps.json`, a plugin's `.mcp.json` and inline
  `mcpServers`) is either local, with `command` and optionally `args`, `env` and `cwd`, or
  remote, with `url` and optionally `headers`; mixing the two, or a `type` that contradicts
  the fields, is an error.

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

### Runtime hooks

A module's runtime hooks (a Claude plugin's `hooks/hooks.json`, or `components.runtime_hooks`)
are Claude Code hooks: Claude Code runs them on its own events, inside the session, with your
permissions. Only `claude-code` installs them; `gemini-cli` and `opencode` report them as
`kind-unsupported`, which `--force` does not override.

- **Where.** At project scope, `.claude/settings.local.json`; at user scope,
  `~/.claude/settings.json`; in both, under `hooks.<Event>`. These are your own settings files,
  not the shared `.claude/settings.json`, because your consent should not reach everyone who
  clones the repository. Each matcher group is one entry lola owns: your other hooks and
  settings, comments included, are left as they are, and uninstall removes exactly lola's
  groups (and an event list or `hooks` object lola created, once it is empty).
- **Variables.** Claude Code gives plugin hooks `CLAUDE_PLUGIN_ROOT` and `CLAUDE_PLUGIN_DATA`,
  but not hooks in settings files, so lola supplies them for every `command` handler: the module
  copy and its plugin-data directory, as absolute paths.
  - `${CLAUDE_PLUGIN_ROOT}`, `$CLAUDE_PLUGIN_ROOT` and `${PLUGIN_ROOT}` (and the `DATA` forms)
    in `command` and `args` become the path. A bare `$NAME` counts only when no letter, digit or
    `_` follows it, so `$CLAUDE_PLUGIN_ROOTS` is left alone.
  - A shell-form command (no `args`) becomes
    `export CLAUDE_PLUGIN_ROOT='<root>' CLAUDE_PLUGIN_DATA='<data>'; <command>`, so every part of
    a compound command, and every script it runs, sees both.
  - An exec-form command (with `args`, even `[]`) becomes `env -- CLAUDE_PLUGIN_ROOT=<root>
    CLAUDE_PLUGIN_DATA=<data> <command> <args>...`, with every value passed as written.
  - Other handler types (`http`, `mcp_tool`, `prompt`, `agent`) and every other field are kept
    as written.
- **What lola will not install** without `--force` (each is a forceable skip of the module's
  hooks): a shell-form hook when the module copy's or plugin-data path has characters other
  than letters, digits and `/ . _ + -` (`claude-code/runtime-hook-path-unsafe`; use exec form,
  or a simpler path; if the path holds `'`, `"`, `$`, `` ` `` or `\`, `--force` does not
  override it, so only exec form installs); an exec-form command containing `=` or starting with `-`, which `env`
  would misread (also `runtime-hook-path-unsafe`); a shell-form hook with `shell: "powershell"`,
  which cannot take the `export` (`claude-code/runtime-hook-shell-unsupported`); and, at
  project scope, a `.claude/settings.local.json` that git could commit
  (`claude-code/settings-local-not-ignored`, below). Even with `--force`, a POSIX shell never
  parses a lola path as shell syntax: an unsafe path is exported in single quotes and referred
  to as `${CLAUDE_PLUGIN_ROOT}`. The shell still splits an unquoted reference at spaces and
  expands glob characters in it, so a forced `${CLAUDE_PLUGIN_ROOT}/x.sh` under a path with a
  space runs a different program. Write hooks with the variable quoted:
  `"${CLAUDE_PLUGIN_ROOT}/x.sh"`. Other shells (PowerShell, for example) get no such guarantee.
- **Windows.** The rewrite assumes a POSIX shell and an `env` program. Windows paths
  (`C:\...`) hold `\`, so shell-form hooks never install there, even with `--force`: give the
  hooks `args` (exec form). An exec-form hook needs an `env` executable, which Git Bash
  provides.
- **Hooks in component frontmatter.** Claude Code also runs a `hooks:` key in a skill's,
  command's or agent's frontmatter while that component is active. lola treats any key that
  equals `hooks` ignoring case as hooks, checks it like `runtime_hooks`, gives its command
  handlers the same variable rewrite (`${PLUGIN_ROOT}` expansion of the rest of the file never
  touches them), and installs the component only under the same consent: the prompt lists its
  hooks under the component's name. Declined, that component is skipped and the rest of the
  module installs. A `.md` file inside a command's own directory (`commands/deploy/notes.md`
  beside `commands/deploy.md`) is a command to Claude Code, so a `hooks:` key there is refused
  when the module loads: put hooks in the command itself. A file whose first line starts with `---`
  but is not exactly `---` is refused too (see Frontmatter, above), because Claude Code might
  read frontmatter there that lola cannot. When lola rewrites the hooks, the component's body
  stays body text: it is never read as frontmatter. Installs made before this version keep
  their components with frontmatter hooks until `lola render`, which asks for consent or
  removes them; a module whose command directory holds a `.md` file with hooks no longer loads,
  and `lola render` stops until you fix or remove it (`lola uninstall <name>`). Consent is yours alone: if your project commits the installed skills,
  commands or agents under `.claude/`, their frontmatter hooks and `allowed-tools` reach your
  teammates, whose Claude Code applies its own workspace-trust rules to them.
- **MCP servers in agent frontmatter.** An agent's `mcpServers:` list (any key that equals
  `mcpServers` ignoring case) gives the agent MCP servers. An item such as `- github` names a
  server you already configured and needs no consent. An item such as
  `- playwright: {command: npx, args: [-y, "@playwright/mcp@latest"]}` defines a server that
  Claude Code starts when the agent starts, so lola installs such an agent only under the same
  consent as hooks: the prompt lists each inline server under the agent's name, with its
  transport, its command line or URL, its headers and env with their values (a value such as
  `LD_PRELOAD=…` changes what runs), and any other field as JSON. Consent covers the servers
  as lola renders them, so changing one, even only its `args`, asks again. Declined, the agent
  is skipped (`claude-code/runtime-hooks-declined`) and one installed earlier is removed. When
  the module loads, `mcpServers` must be a list; each item must be a non-empty name or a
  mapping of exactly one server name to its definition; the definition gets the checks of
  `mcps.json` (one transport's fields, a `type` that agrees with them, a valid server name).
  A server name may be defined once per agent, and not both defined and named there.
  Installs made before this version keep such agents until `lola render`, which asks for
  consent or removes them.
- **Permission mode.** An agent's `permissionMode` sets the permission mode Claude Code runs it
  in. lola installs it as written and reports `claude-code/permission-mode`, naming the agent
  and the mode. Claude Code v2.1.267+ ignores `bypassPermissions` in a subagent's frontmatter.
- **Pre-approved tools.** A skill's, command's or agent's `allowed-tools` lets Claude Code run
  those tools without asking. lola installs it as written and reports
  `claude-code/allowed-tools`, naming the component and every tool it lists. A `.md` file in a
  command's own directory is a command to Claude Code too, so its `allowed-tools` gets the same
  warning, naming that file.
- **Git.** When the project is in a git work tree, lola checks that git ignores
  `.claude/settings.local.json` and does not track it (`git check-ignore`, `git ls-files`, with
  the project's git configuration unable to run programs). Claude Code ignores the file only
  when it creates the file itself, so add `.claude/settings.local.json` to your `.gitignore`. If
  git is missing or fails, lola skips the hooks too.

**Consent.** lola installs a module's runtime hooks only with your consent:

- On a terminal, lola lists each hook (its event, matcher and type, `if`, `async` and `shell`
  when set, then the command line it runs, the URL an `http` hook sends data to with its headers
  and the environment it sends, the MCP tool it calls, or the prompt, cut at 200 characters;
  then any other field of the handler or of its matcher group, as JSON) and each inline MCP
  server of an agent, and asks "Install <module>'s Claude Code hooks?" ("…hooks and agent MCP
  servers?" when it lists a server), defaulting to No. A yes is recorded in
  `$XDG_STATE_HOME/lola/consent.json`.
- The terminal view shows each value on one line, and a newline inside a value appears as the
  two characters `\n`, so a value cannot pass as another hook line. Control characters,
  invisible format characters (bidirectional overrides, zero-width characters, a byte-order
  mark) and line or paragraph separators in a value show as `?`, so text looks like what it is.
  Names, paths and manifest metadata (a module's description and version) get the same treatment where lola prints them; other module text, such as frontmatter keys quoted in finding texts, does not yet. `--json`
  consent values are replaced the same way; the exact group lola writes is the step's `member`
  (`steps[].member`).
- If the prompt fails (input closes, for example), the run stops: nothing is written and nothing
  is recorded.
- Off a terminal, `--allow-runtime-hooks` records consent to the hooks the summary lists.
  `--allow-hooks` is for install hooks and never covers runtime hooks, and `-y` never grants
  consent.
- `--dry-run` makes the decision a real run would make (a recorded consent or
  `--allow-runtime-hooks` installs the hooks; anything else declines them), lists the hooks,
  and never asks or records.
- Consent is recorded per module and settings file (the absolute path of
  `.claude/settings.local.json` or `~/.claude/settings.json`, symlinks resolved), and covers the
  hooks as lola renders them, paths included, and the module version they come from. A new
  version asks again, and so does another project, the other scope, or the same project after
  you move it: off a terminal, its hooks are removed until you consent with
  `--allow-runtime-hooks`. Consenting in one project leaves the others' consent alone.
- Without consent the hooks are skipped with `claude-code/runtime-hooks-declined`, and the rest
  of the module installs (`--strict` makes that an error). Hooks lola installed under an earlier
  consent are removed: they never keep running under a consent that did not cover them.
- A hook group is identified by its content, so a group you edit or remove is no longer lola's
  copy. `lola install` and `lola render` do not write it again, since both copies would run:
  lola keeps it as owned but absent and reports `claude-code/runtime-hook-drift`, with the hint
  to run with `--force`, which writes it again. The report is info, not a warning: it records
  your choice, so `--strict` does not fail on it. Uninstall leaves your edited group in place.
  (Missing files and JSON keys are rewritten as before; this rule is only for hook groups.)

## Scopes and targets

- **Project scope**: files go into the project; state lives in `.lola/`. For `install`, see
  [Which scope](#which-scope). Every other command acts on the nearest parent directory with a
  `.lola/`, else the current directory; `-C <dir>` picks one explicitly.
- **User scope** (`--user`): files go into your home directory (OpenCode's into
  `$XDG_CONFIG_HOME/opencode/`); state lives under `$XDG_CONFIG_HOME/lola/` (manifest
  `user.yml`) and `$XDG_STATE_HOME/lola/` (ledger `lock.json`). Rules installed at user scope
  apply in every project.

The targets are `claude-code` (Claude Code), `gemini-cli` (Gemini CLI) and `opencode`
(OpenCode).

When you do not pass `-t`, lola uses the manifest's targets. Otherwise it offers the assistants
installed on your machine (their command, `claude`, `gemini` or `opencode`, is on your
`PATH`): on a terminal you pick from them, with those already set up in the project preselected,
or every installed assistant preselected when none is set up (with only one installed, lola
uses it without asking); off a terminal lola uses the ones both installed and set up. An assistant is set up in a project when it has `.claude/` or
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

## Choosing what to install

### The install flow

`lola install` with no argument, on a terminal, asks what it needs, one step at a time, inline
on stderr (no full-screen interface):

1. **Scope**: **Project** (showing its root), **User** (`~`) and **Other directory…**, in that
   order; tick any of them, at least one. Nothing is preselected, not even when `config.yaml`
   sets `default_scope` (it applies only off a terminal) or the project already uses lola.
   **Other directory…** then asks for a path: a relative one starts from the current
   directory, and `~` is not expanded. It must be an existing directory, not the project
   you ticked above, and not your home directory (that is the user scope; tick User instead);
   otherwise lola says why and asks again. Ticking Project where the project is your home
   directory is asked again the same way. That directory is installed into
   as a project of its own, so one run can install into this project, another one and your
   user scope at once.
2. **Modules**: every module in your cache, as `source · name — description`, filterable by
   typing. Modules already installed in a chosen scope are marked `(installed)`; picking one
   reconfigures it. The last entry, **Add a source…**, asks for a source and an optional ref,
   adds it to your cache exactly like `lola cache add`, and shows the list again with it
   selected. Leaving the source empty returns to the list with the modules you ticked still
   ticked. With an empty cache you get Add a source… or Quit.
3. **Assistants**: the installed assistants, as described under
   [Scopes and targets](#scopes-and-targets).
4. **Components**: one page per module, a list per kind (skills, commands, agents, MCP
   servers, rules), with everything preselected except what `selections.yaml` skips (labelled
   `(skipped in selections.yaml)`). MCP servers that run a command say `(runs a command)`.
   Rules are shown disabled under `--no-rules` (or when the module's rules are off). A module
   with runtime hooks ends its page with "Runtime hooks: N (you will be asked before they are
   installed)". Unpicking everything asks "Install <module> with no components?"; No leaves
   the module out of this run. With several destinations, a module whose preselection is the
   same everywhere gets one page for all of them; otherwise it gets one page per destination,
   titled `<module> — project (<root>)` or `<module> — user`.
5. **Summary**, then a menu: **Apply**, Change scope, Change modules, Change assistants,
   Change components (each offered when that step was asked), or Quit. Changing a step asks
   it again with your answers preselected; later answers that still apply are kept. Change
   scope keeps the component picks of the destinations you keep, and the summary counts the
   picks of the ones you remove as dropped.

A step with no real choice is not shown: a scope, module, assistant or component flag answers
it, as does a single installed assistant or a module with one component. The menu appears
only when a step was shown; `-y` applies without it. The install uses exactly the versions
the pickers listed, for source arguments and for modules picked from the cache, and keeps
them through the menu's "Change …" steps. If a listed version was removed from your cache in
the meantime, the run stops and asks you to run `lola install` again. Two arguments that
resolve to different versions of one module are refused. When nothing would change, lola says
`already up to date`. Runtime hook consent is asked during the install, as always.

The summary lists, per destination and assistant, each module's components: `+` added, `~`
updated (a newer cached version), `-` removed, `= N unchanged`; where each destination came
from; the `selections.yaml` entries the run writes; and the **equivalent commands**, which do
the same without a terminal:

```
Equivalent commands:
  lola cache add ../src/deploy
  lola install review-kit deploy --scope project -t claude-code --skip review-kit:skill/slow-lint
```

With more than one destination there is one `lola install` line per destination: each
project gets `--scope project -C <root>` and the user scope gets `--scope user`. There is no
flag for several project roots in one run; `--scope both` still installs into one project
and your user scope.

Equivalent commands, and commands inside hints, are quoted to paste into a POSIX shell (bash,
zsh) on Linux and macOS, and into PowerShell 7.3 or later on Windows. There an argument goes
in single quotes when it holds anything beyond letters, digits and `_ - . / : = +`, or starts
with `-` and holds a `.` (`-t 'claude-code,gemini-cli'`). Windows PowerShell 5.1 drops empty
arguments (`''`) and mangles embedded `"` when it runs a program.

When the `-C` path contains control or format characters, the line is shown with them
replaced by `?`, followed by a note that it is not copyable as shown.

Ctrl+C at any step cancels: "aborted; nothing changed". Nothing is written before Apply,
except that Add a source… (and a source you name as an argument) is fetched into your
cache, which installs nothing. After Apply, a runtime-hook consent you give (or
`--allow-runtime-hooks`) is recorded in `consent.json` when you give it, before
`selections.yaml` and the installed files are written; if the run then stops (a refusal, or
a later prompt you decline), that consent stays recorded and nothing else is written.
`--dry-run` runs the steps and the summary and writes nothing, not even `selections.yaml`.

`LOLA_ACCESSIBLE=1` (or `TERM=dumb`) asks the same questions as numbered line prompts, for
screen readers; there, end of input at any prompt, runtime hook consent included, cancels the
run ("aborted; nothing changed"), and Ctrl+C interrupts.

### Which scope

`install` never picks a scope silently. First match wins:

| You give | Scope |
|---|---|
| `--scope project\|user\|both` (also `project,user`) | that; `-C <dir>` sets the project |
| `--user` | user (with `--scope`, they must agree) |
| `-C <dir>` | the project at `<dir>` |
| a terminal | the scope step, with nothing preselected |
| nothing, in or below a directory with `.lola/` | that project; stderr says `scope: project (found .lola/ at <path>)` |
| `default_scope` in `config.yaml` | that scope |
| none of these | an error naming `--scope`, `--user`, `-C` and `default_scope` |

`--scope project` without `-C` uses the nearest parent with a `.lola/`, else the current
directory. `--scope user -C <dir>` is an error.

You cannot install a project at your home directory: it renders exactly where the user scope
does (`~/.claude`, …), so the two would claim the same files. `install` refuses it however it
is chosen (`-C ~`, `--scope project` or `--scope both` run from `~`, or a `.lola/` found in
`~`), with a hint to use `--user` instead. `render` and `uninstall` still work on a home
project an older lola installed, so you can remove it. `--json` output lists every destination in
`scopes`, each with its `scope`, its `root` (for a project) and its `scope_source`: `flag`,
`config`, `discovered` or `picked`. Modules and steps carry the same `scope` and `root`.
`default_scope` is only read off a terminal; on one, the scope step asks.

**Breaking change:** `lola install` off a terminal, in a directory with no `.lola/` above it
and no `default_scope`, no longer installs into the current directory; pass `--scope project`
(or set `default_scope: project`). `render`, `uninstall`, `list` and `check` keep the old
resolution, since they act where lola already installed something.

### Component flags

| Flag | Effect |
|---|---|
| `--skip <selector>` | Do not install the matching components; saved as a skip (and a matching include in that section is removed). |
| `--only <selector>` | For each module with a match, skip every component that does not match and unskip those that do. Saved as skips of the components that exist now, so components a later version adds are installed. Warns when a wider include in the section keeps a non-matching component installed. |
| `--unskip <selector>` | Install skipped components again: removes the matching skips, and where a global skip still applies, adds an include. With `--global`, warns when a scope section still skips the item. |
| `--global` | Save the above in the global section instead of the scope's. |

A selector is `kind/name` or `module:kind/name`, where `kind` is `skill`, `command`, `agent`,
`mcp` or `rule` and `name` may be a glob (`review-*`). A bare selector applies to every module
being installed that has a match; one that matches nothing is an error that lists that kind's
components (module-qualified). All three repeat, and always write `selections.yaml` (except
under `--dry-run`), saying `→ saved to <path>`. With more than one destination (`--scope
both`, or several ticked in the scope step) they need `--global`, since one flag cannot say
which scope it means. `--no-rules` wins over rule selections. Names in
selectors, from flags or the file, may not contain control or format characters.

```sh
lola install review-kit --scope project --skip skill/slow-lint --skip 'mcp/internal-*'
lola install review-kit --scope user --only skill/review --only command/ship
lola install review-kit --scope project --unskip review-kit:skill/slow-lint
lola install review-kit deploy --scope both --skip command/deploy --global
```

### `config.yaml` and `selections.yaml`

Both live in `$XDG_CONFIG_HOME/lola/` (normally `~/.config/lola/`), are safe to edit by hand,
and are checked strictly: unknown keys, wrong types, unknown kinds, bad globs, relative or
`~` project keys, two keys for one project, control or format characters in selector names,
an `include` in the `global` section, anchors, aliases and merge keys, and files over
1 MiB are errors that name the file, line and key, with a hint.

```yaml
# lola settings. Safe to edit by hand.
version: 1
default_scope: project   # project | user | both; read off a terminal only
```

`selections.yaml` is the one place that says which components you do not want. `install`,
`render` and `check` all honor it, so editing it takes effect on the next `render` (a
component you skip after installing it is removed, unless you edited its file, which is kept
and reported):

```yaml
# lola selections: components you chose not to install.
version: 1
global:
  review-kit:                       # module name as `lola list` shows it
    skip: [skill/slow-lint, mcp/internal-db]
user:
  review-kit:
    include: [mcp/internal-db]      # overrides a global skip at user scope
projects:
  /home/me/src/api:
    review-kit:
      skip: [command/deploy]
      include: [skill/slow-lint]
```

A module's exclusions at a scope are the global skips, plus that scope's skips, minus that
scope's includes (`global` has no includes). Project keys are absolute paths; a key written
through a symlink or with `..` still matches its project. A selector naming a component the
module no longer has is a warning. lola writes the file under a lock, replacing only the
entries of the modules it changes, so your comments, order and other edits survive, even in a
file that holds only comments or a null document. A section or project key that a removal
empties is dropped, unless it carries a comment, in which case it stays as an empty mapping.
A symlinked file is written at its target. A file that does not parse is never overwritten.
Entries for projects you moved are left alone.

### Off a terminal

Every answer has a flag, so scripts and CI never need a terminal. Off one (or with `--json`)
lola never asks; a missing answer is an error whose hint names the flag: no module →
`lola install <source|name>`; no scope → see [Which scope](#which-scope); no assistant found →
`-t`. Copy the equivalent commands from an interactive run to repeat it.

## Findings

When a target cannot express part of a module, lola tells you instead of failing the whole
module. Each **finding** has a severity:

- **info**: a lossless adaptation, or a choice you made. Shown by `lola check` and in `--json`
  output, and by `install` and `render` when it carries a hint.
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
| `kind-unsupported` | skip | no | The target has no place for this kind of artifact (in this build, `gemini-cli` and `opencode` for a module's runtime hooks). |
| `name-rejected` | skip | yes | The target rejects the artifact's name (for `opencode`, also a skill whose `name` differs from its directory). |
| `required-key-missing` | skip | yes | A frontmatter key the target needs is absent, empty, or was dropped for having the wrong type. |
| `agent-tools-unenforceable` | skip | yes | The agent restricts its tools in a way the target cannot enforce (`gemini-cli`: names that are not Gemini's; `opencode`: any non-blank `tools` value, list or string). The assistant would give the agent every tool, so it is left out unless `--force`, which installs it without the list. |
| `frontmatter-invalid` | skip | no | The frontmatter parses but lola cannot re-encode it. |
| `frontmatter-key-dropped` | warn or info | no | The target does not support a frontmatter key, so it is left out. |
| `frontmatter-value-dropped` | warn | no | A frontmatter key has a value of the wrong type or form for the target (for example an OpenCode agent `color: red`, or a Gemini agent `max_turns: 1.5`), so it is left out. |
| `frontmatter/yaml-ambiguous` | warn | no | An unquoted frontmatter value that YAML parsers read differently; quote it. |
| `version/unsupported` | warn | no | Your installed assistant is older than the version lola renders for and ignores something the module uses. |
| `version/limited` | warn | no | Your installed assistant has a known limitation that affects this module. |
| `module-warning` | warn | no | The module itself has a problem, such as a legacy instruction file, a skipped rule entry or skill, an ignored manifest, or a plugin key lola does not install. |
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
| `claude-code/runtime-hooks-declined` | warn | no | You did not consent to the module's runtime hooks, so they (or the skill, command or agent whose frontmatter has hooks, or the agent with inline MCP servers) are not installed and any installed earlier are removed; rerun with `--allow-runtime-hooks`, or answer the prompt on a terminal. |
| `claude-code/allowed-tools` | warn | no | A skill, command or agent pre-approves tools with `allowed-tools`; Claude Code runs them without asking. Installed as written. |
| `claude-code/permission-mode` | warn | no | An agent sets the permission mode Claude Code runs it in with `permissionMode`; the finding names the mode. Claude Code v2.1.267+ ignores `bypassPermissions` in a subagent's frontmatter. Installed as written. |
| `claude-code/runtime-hook-path-unsafe` | skip | yes, unless a path holds a quote, `$`, a backquote or `\` | A shell-form hook needs the module copy and plugin-data paths to hold only letters, digits and `/ . _ + -`, or an exec-form command contains `=` or starts with `-`. |
| `claude-code/runtime-hook-shell-unsupported` | skip | yes | A shell-form hook runs in PowerShell, which cannot take the `export` lola adds; give it `args`. |
| `claude-code/settings-local-not-ignored` | skip | yes | Git could commit `.claude/settings.local.json` (tracked, not ignored, or git missing or failing); add it to `.gitignore`. |
| `claude-code/runtime-hook-event-invalid` | skip | no | A hooks event name is not an identifier, so it is no event Claude Code fires. |
| `claude-code/runtime-hook-drift` | info | no | A hook group lola installed was edited or removed. On install and render lola leaves it out until `--force` writes it again, and prints the finding with that hint; on uninstall lola leaves your edited group alone. `--strict` does not fail on it. |
| `probe/not-found` | info | no | The assistant is not on `PATH`; lola checked against the newest version it knows. |
| `probe/unknown-version` | info | no | The assistant's version could not be read. |
| `probe/cache` | info | no | The version was read but could not be cached. |

lola only reports what its own rendering loses. For general advice on writing skills, commands
and agents, use a dedicated linter such as [skillsaw](https://github.com/stbenjam/skillsaw).

## Coming from the Python lola

lola does not read the Python lola's state (`~/.lola/installed.yml`, its module registry and
marketplaces), and the Python lola does not read lola's. `lola migrate` moves you over once:

```
lola migrate --dry-run    # the plan; writes nothing
lola migrate              # the plan, then "Migrate?" (default No); off a terminal, add -y
```

It reads the Python lola's home (`$LOLA_HOME`, else `~/.lola`) and, for every project and for
user scope where the Python lola installed a module for Claude Code, Gemini CLI or OpenCode,
installs the same module with lola, from the same source (with its ref and content directory
as `--ref` and `--path`; a content directory of `/` is `--path .` for a folder or git source),
on the same assistants; a module lola already has there stays at the version it has. Before it
plans anything else, lola fetches each source and checks that the module it finds there has
every skill, command, agent and MCP server the Python lola recorded (a skill it renamed to
`<module>_<skill>` counts as `<skill>`); if not, lola found a different module root (the hint
gives the `--path` to try) or the source no longer ships it
(the hint says what to do with the Python lola's files), and the record is not migrated. A
source lola cannot fetch (offline, say) leaves its records unmigrated the same way, with the hint
to rerun `lola migrate` once the source is reachable; the module's other records in that place
stay with them, and the rest of the plan goes on. An
archive's content directory below `/` cannot be mapped (the Python lola looked it up below the
module it found in the archive); install such a module yourself with `--path`. Then it cleans
up:

- **Replace with backup.** A file the Python lola recorded and lola now writes (a skill, a command
  and its sidecar directory, an agent, an MCP server entry at the same place) is copied into the
  trash first, then lola installs over it. The Python lola transformed what it wrote, so lola
  cannot tell your edits from its own: the copy keeps them. A file in a replaced directory that
  lola does not write is then removed if it still matches the copy, and kept and listed if it
  changed; with `--keep-python` every such file stays and is listed.
- **Moved to the trash.** A recorded file lola does not write (a renamed component, OpenCode's
  command sidecars and legacy `.opencode/command/` and `.opencode/agent/` files) and the module
  copies in `.lola/modules/` go to the trash. A module copy stays while something the Python lola
  left in place still uses it.
- **Kept and listed.** Symbolic links (and paths below one), files git tracks (read by their
  literal names, so `*` or `[` in a name is no pattern; unless `--force`; inside a git work tree
  where git is missing or fails, they stay), anything a lola module owns, and, with
  `--keep-python`, everything lola did not replace. When lola would write such a file (one git
  tracks, or one below a symbolic link), the plan refuses and names the file and why it is kept:
  `--force` backs it up into the trash and replaces it, reported as replaced; for a symbolic link
  you can instead replace the link with a directory.
- **The Python home** goes to the trash last, only when every record migrated, nothing failed, and
  it holds nothing but what the Python lola writes there. While a `~/.lola` stays, lola sees a
  project at `~` and refuses to install there; the output says why it stayed.

**Not migrated, never changed** (each is listed with its file, and how to remove it by hand):
installs for Cursor, Copilot and OpenClaw (their files stay exactly as they are); modules with
no recorded source or a source lola cannot fetch (`git://`, `.tar.bz2`, …); instruction
blocks (`<!-- lola:module:<name>:start -->` in `CLAUDE.md`, `GEMINI.md`, `AGENTS.md`, and at
user scope `~/.claude/CLAUDE.md`, `~/GEMINI.md`, `$XDG_CONFIG_HOME/opencode/AGENTS.md`), which
keep working; Gemini's skill sections in `GEMINI.md`; MCP entries lola does not write (the
Python lola put Claude Code's user-scope servers in `~/.mcp.json`, lola uses `~/.claude.json`);
plugin data (`.lola/plugin-data/<module>`), which MCP servers keep using; marketplaces; and
`.lola-req`. lola keeps rules as files of their own instead of blocks in your instruction files
([decisions](decisions.md)), so move a module's `AGENTS.md` to `rules/` to ship it with lola.
A module's installs in one project (or at user scope) migrate together or not at all: when one
of them cannot be migrated (say, the module is also installed for Cursor there), the others stay
with it, since they share the Python lola's copy of the module.

**Before anything is written** every destination is planned and every hook question asked: a
conflict lola may not resolve (a file in the way that the Python lola did not record, or a
directory in `.lola/modules/` that an install would replace and no migrated record accounts for,
or one it would swap out that git tracks or that is a symbolic link; `--force` backs such a copy
up and reports it replaced), a hook without consent, or a cancelled prompt changes nothing.
`--force` overwrites such files as `lola install --force` does, after copying each into the trash,
and lists them as replaced. When two modules the Python lola installed in one place ship a
component of the same name, the Python lola skipped one; lola refuses, naming both modules and the
component, and gives the Python lola's `lola uninstall <module> -a <assistant> <project>` command
for the module it skipped the component in: run it with the Python lola, then rerun
`lola migrate`, which migrates the other module. After that, one destination that fails stops
before it writes and the others go on; rerunning finishes the job, and a second run says "nothing
to migrate". Like `lola install --dry-run`, the plan fetches the modules into your cache; the run
you confirm installs exactly the versions the plan showed, and refuses one removed from the cache
in the meantime.

**The trash** is `$XDG_STATE_HOME/lola/trash/<UTC time>/`: one directory per destination
(`user`, or a hash of the project's path) mirroring each path below the project or your home,
the Python home as `python-home/`, and `manifest.txt`, one `original<TAB>trash path` line per
entry. Restoring is a move back; lola never deletes the trash. lola refuses to write through a
symbolic link inside the trash ("nothing was moved"), and a move across file systems syncs the
copy's directories before it removes the original.

## Files lola keeps

| File | Purpose | Commit it? |
|---|---|---|
| `.lola/project.yml` | The manifest: which modules and targets you want. | Yes |
| `.lola/lock.json` | The ledger: every file and JSON key lola wrote, with hashes. | No (gitignored for you) |
| `.lola/modules/` | Module copies that `${PLUGIN_ROOT}` points into. | No (gitignored) |
| `.lola/plugin-data/` | `${PLUGIN_DATA}` directories. | No (gitignored) |
| `.claude/settings.local.json` | Claude Code's per-user settings, where lola puts runtime hooks you consented to. | No: add it to `.gitignore` |
| `$XDG_CONFIG_HOME/lola/config.yaml` | Your settings: `default_scope`. | Yours to keep |
| `$XDG_CONFIG_HOME/lola/selections.yaml` | Components you chose not to install, globally, at user scope and per project. | Yours to keep |
| `$XDG_STATE_HOME/lola/trash/` | What `lola migrate` backed up or moved, one directory per run with `manifest.txt`. | Yours; delete a run when you no longer need it |

lola owns `.lola/.gitignore` and rewrites it if it changes. The lock lola holds while it
changes a project lives outside the project, in `$XDG_STATE_HOME/lola/locks/`.

## Safety rules

- lola writes a file only if it is new, already identical, or one lola wrote and nobody edited.
  Anything else is a **conflict**: lola lists every conflict and changes nothing.
  `--force` overrides conflicts and lists each overridden file in the summary.
- A destination owned by another installed module is never overridden, even with `--force`.
- A runtime hook group already in your settings, identical to one a module renders, is a
  conflict: owning it would remove your hook on uninstall. `--force` takes it over.
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
- Without a terminal, `uninstall`, `cache rm` and `migrate` need `-y`.
- `install --json` adds `scopes` (each with `scope`, `root` and `scope_source`), and per
  module `scope`, `skipped` (component names, `[]` when none) and `saved_to` (the
  `selections.yaml` it wrote, or `""`), and `equivalent_commands` (`[]` when no step was shown).
  `list --json` and `check --json` carry `skipped` per module.
- `migrate --json`: `dry_run`, `nothing_to_migrate`, `python_home`, `trash` (`""` when nothing
  was put there), `home` (`action` `move`, `keep` or `none`, with `reason` and `trash`),
  `unmappable` and `not_migrated` (each entry with its `hints`), `warnings`, and per
  destination its `status` (`planned`, `migrated`, `unchanged` or `failed`, with `error` and
  `hints`), `installs`, `replaced` and `moved` (each `path`, `key`, `kind`, `trash`), `kept`
  (`path`, `reason`), its own `unmappable` and `not_migrated`, and the install's `steps`,
  `modules`, `findings` and `notices`. A step that replaces a recorded file has
  `"replaced": true`.

## Environment

| Variable | Used for |
|---|---|
| `HOME` | User-scope root; base of the XDG defaults |
| `PATH` | Where lola looks for installed assistants; relative entries are ignored |
| `XDG_CONFIG_HOME` | User-scope manifest, `config.yaml`, `selections.yaml`, and OpenCode's user-scope directory (default `~/.config`) |
| `LOLA_HOME` | The Python lola's home, which `lola migrate` reads (default `~/.lola`; must be absolute) |
| `LOLA_ACCESSIBLE` | `1` draws the install flow as numbered line prompts (screen readers) |
| `TERM` | `dumb` does the same as `LOLA_ACCESSIBLE=1` |
| `LOLA_COLOR`, `NO_COLOR` | Color of output and of the install flow's forms (see the output contract) |
| `XDG_DATA_HOME` | User-scope module copies and plugin data (default `~/.local/share`) |
| `XDG_STATE_HOME` | The user-scope ledger, hook and runtime hook consent, lock files and `lola migrate`'s trash (default `~/.local/state`) |
| `XDG_CACHE_HOME` | The cache, download scratch space and assistant version cache (default `~/.cache`) |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Archive downloads (git reads them itself) |
| `GIT_*`, `SSH_AUTH_SOCK` | Passed to git, except the variables that pick a repository or add config behind lola's back (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_CONFIG_PARAMETERS`, `GIT_CONFIG_COUNT`, …) |

Relative XDG values are ignored, as the XDG specification requires.
