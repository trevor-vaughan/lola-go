# Module layout

[`lola init`](../usage.md#lola-init) scaffolds a module, and [`lola check`](../usage.md#lola-check)
reports what each assistant would drop, adapt or skip from it. Templates are in
[templating.md](templating.md).

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

Any of these Markdown files may instead be a template, `<name>.md.liquid` ([Templates](templating.md#templates)).

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
vars:                           # what templates read as vars, with defaults (templating.md, Vars)
  language: go
  strict: false
  tools: [rg, jq]
```

[Vars](templating.md#vars) describes `vars:`.

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

**Rules.** Each `rules/<rule>.md` is always-on context: your assistant loads it in every session.
lola delivers rules where the assistant loads them natively (see the tables in [Scopes and
targets](../usage.md#scopes-and-targets)) and never edits your `CLAUDE.md`, `GEMINI.md` or
`AGENTS.md`. Gemini CLI reads them only in folders you trust, and project rules only when it starts
in the project root. Frontmatter is removed; a `paths:` key is an error, because guidance for some
files belongs in a skill. The rest of a rule is delivered verbatim: `${PLUGIN_ROOT}` is not expanded
in rules. A rule whose remaining text starts with `---` gets an empty `---` block first, so that
text is never read as the rule's frontmatter. A module-root `INSTRUCTIONS.md` or `AGENTS.md` is not
installed; lola warns and suggests moving it to `rules/`. `lola install --no-rules` installs
everything else and records `rules: false` on the module's manifest entry, so later renders keep it
off; `--rules` turns them back on.

## Module formats

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

## `components`

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
remembers a yes until a hook script changes. Before a hook runs, lola replaces the module copy
from the version it verified, so hooks never run in an edited copy; reinstalling the same version
with no hook to run (no consent, say) keeps the copy and what its hooks built. Consent covers the
hook scripts themselves, not other files they run, so review those too. `--allow-hooks` consents for one
run; `-y` never does.
Without consent a `pre-install` hook stops the install before anything is written, and a
`post-install` hook is skipped with a warning. A failing `pre-install` hook installs nothing; a
failing `post-install` hook leaves the installation in place with a warning. `lola render` runs
hooks only for modules whose version changed or that gain a target, and `--dry-run` lists hooks without running them
(`would run`). A `post-install` hook skipped without your consent stays **pending** in
`lock.json` (`hooks_pending`): `lola sync <module>` replaces the module copy from the version lola
hashed, so an edited copy never runs, and asks again showing the scripts; off a terminal,
`lola sync <module> --allow-hooks` runs it. A sync that does not name the module, and is not
given `--allow-hooks`, asks nothing about the hook and says
`pending post-install hook for <module> → lola sync --project <module>` (off a terminal, with the
`--allow-hooks` form); a dry run says so too, and, naming the module, shows the hook it would
run.

## Runtime hooks

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
  (`claude-code/settings-local-not-ignored`, in [Findings](../usage.md#findings)). Even with
  `--force`, a POSIX shell never parses a lola path as shell syntax: an unsafe path is exported in single quotes and referred
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
  consent or removes them. A skill or command's `mcpServers` (any spelling) is dropped with
  `frontmatter-key-dropped`: Claude Code starts servers only from an agent's frontmatter.
  Gemini CLI and OpenCode run no hooks and start no servers from skill, command or agent
  frontmatter, so lola drops `hooks` and `mcpServers` (any spelling) there the same way; a
  Gemini agent's own `mcp_servers` is dropped too, as is every agent key outside the set lola
  keeps for gemini-cli, and so is `mcp_servers` (any spelling) on a Gemini skill or command. No key that starts a process reaches an assistant without consent.
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
