# Adding or replacing an assistant with a target spec

A *target* is an assistant lola installs modules into. Each one is a YAML *spec* that says where the
assistant reads skills, commands, agents, MCP servers and rules, how lola finds it, and what its
frontmatter accepts. lola ships specs for `claude-code`, `gemini-cli`, `opencode` and `agents`. You
can add a spec for a tool lola does not know, or write one with a built-in's id to replace it. No
Go change and no rebuild is needed.

Skip this guide if you only install modules: the built-in targets need nothing from you.

## Where specs live

Put one file per target in `$XDG_CONFIG_HOME/lola/targets/` (normally `~/.config/lola/targets/`).

- Only `*.yaml` files are read. A `.yml` file is refused, so a spec never goes unread by
  mistake: the error is `<file> is not read`, with the hint `rename it to <name>.yaml: lola reads
  target specs only from .yaml files`. Other files, and subdirectories, are ignored.
- Names starting with `.` (an editor's lock or swap file, such as Emacs's `.#quill.yaml`) are
  skipped, and so is anything that is not a regular file once symlinks are followed: a FIFO, or a
  symlink to nothing. A symlink to a spec file is read.
- The `id:` inside the file is the target's name, not the file name. Two files with one id are an
  error.
- If `targets` exists but is not a directory, every command stops and says so.

`lola install --help` lists the targets, built-ins first (`claude-code`, `gemini-cli`, `opencode`,
`agents`), then your own by id. `-t <id>` and `targets:` accept your id like any other.

## A complete example

`quill` is an editor lola has no built-in for. This file is
`~/.config/lola/targets/quill.yaml`:

```yaml
id: quill
readers: "the quill editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      skill: {mech: skill-dir, path: .quill/skills}
      command: {mech: markdown-file, path: .quill/commands, ext: .md, sidecar: drop}
      agent: {mech: markdown-file, path: .quill/agents, ext: .md}
      rule: {mech: rules-dir, path: .quill/rules/lola}
```

With a `.quill/` directory in a project, `lola install ./pen -t quill` writes:

- a skill to `.quill/skills/<skill>/`
- a command to `.quill/commands/<command>.md`
- an agent to `.quill/agents/<agent>.md`
- a rule to `.quill/rules/lola/<module>/<rule>.md`

The spec places no `mcp` kind, so a module's MCP servers are reported as something quill cannot
take (a `kind-unsupported` finding) and the rest installs. `lola uninstall pen` removes the files.

## Fields

| Field | Meaning |
|---|---|
| `id` | The target's name: lowercase letters, digits and single inner hyphens (`my-tool`). |
| `readers` | For a target with no `exec` probe: what reads the files it writes. Required then, and refused when there is an `exec` probe. |
| `kind_unsupported_note` | Optional text added to the finding for an artifact of a kind the target does not place. |
| `probe` | How lola reads the installed assistant's version: a list of methods, `exec` and `file`, tried in order. Omit it for a tool with no binary and no version file. |
| `roots` | The directory lola writes into: `project:` and `user:`, each a root variable alone or followed by a clean relative path (`{config_home}/mytool`). |
| `detect` | How lola decides the assistant is set up, per scope (`project:` and `user:`). |
| `eras` | What the assistant reads from a version on. At least one. |

### `probe`

```yaml
probe:
  - exec: {binaries: [mytool], args: [--version], match: '^v?(\d+\.\d+\.\d+)'}
```

lola runs the first of `binaries` found on `PATH` (a command name, never a path) with `args`.
`match` is a Go (RE2) regular expression whose first group is the version.

A tool that keeps its version in a file takes a `file` method, alone or after an `exec`:

```yaml
probe:
  - exec: {binaries: [mytool], args: [--version], match: '^v?(\d+\.\d+\.\d+)'}
  - file: {path: '{config_home}/mytool/state.json', pointer: /version}
```

- `path` starts with `{home}/`, `{config_home}/` or `{data_home}/`, then a clean relative path.
- Set exactly one of `match` (a regular expression whose first group is the version, run over
  the file's text) and `pointer` (an RFC 6901 JSON pointer, such as `/app/version`, to a string
  in the file's JSON).
- lola reads only a regular file of at most 1 MiB. A larger file, a non-file, text with no
  version or a JSON file without a string at the pointer gives a `probe/unknown-version` note.

lola tries the methods in order and uses the first version one yields. A method whose binary is
not on `PATH`, or whose file does not exist, is passed over to the next; if none yields a
version, the note is `probe/unknown-version` for the first method that failed, else
`probe/not-found`. A target with no `exec` method needs `readers`, so a file probe alone still does.
The version read only adds findings; it never changes what lola writes (see `eras`).

### `roots`

`project` may use `{project}`; `user` may use `{home}`, `{config_home}` or `{data_home}`. Nothing
else, and the path after the variable cannot hold `..` or another variable.

### `detect`

Each scope takes exactly one of:

- `any: [markers]`: set up when any marker exists under the root. A marker ending in `/` must be a
  directory.
- `dir: <path>`: set up when that directory exists under the root. `dir: .` means the root itself.

Markers and `dir` are clean relative paths that stay inside the root.

### `eras`

An era is what the assistant reads from version `since` on. Eras go in order of increasing `since`
(`"1.0"`, `"2.1.3"`).

The era lola renders for comes from the version the project or you declare for the target in
`target_versions` (`.lola/project.yml` at project scope, `config.yaml` at user scope; see
[Declaring assistant versions](../usage.md#declaring-assistant-versions)): the last era whose
`since` is at or below it. With nothing declared, lola renders for the newest era. The installed
version is never used to choose by itself; it only adds findings when it falls in another era.
`lola install` is the one command that can turn it into a declaration: for a target with more than
one era and no declared version it asks which era to write for (preselecting the installed one),
or takes `-t <id>@<version>`, and records the answer in `target_versions` ([Picking a version at
install](../usage.md#picking-a-version-at-install)). Era labels in that question come from the
spec's `since` values: `>=2.0` for the newest and `>=1.0 <2.0` (its since, then the next era's)
for the others, each followed by ` — <summary>` when the era has one. A declared
version below the first era selects the first, with a warning. To add support for a newer
version, add an era with `extends: previous`; projects that declare an older version keep the
earlier output.

| Field | Meaning |
|---|---|
| `since` | The first version this era applies to. |
| `summary` | Optional. What this era reads differently, in plain text of at most 80 characters, shown beside the era's label when `lola install` asks which version to write for. An era with `extends` keeps the previous era's summary unless it gives its own. |
| `extends: previous` | Start from the era before and change only what differs. Not allowed on the first era. |
| `remove` | With `extends`, delete from the previous era: `layout.<kind>`, `kinds.<kind>.keys.<key>` or `behaviors.<id>`. |
| `vars` | Your own path variables, `{project: ..., user: ...}` each, for use in `layout`. |
| `layout` | Where each artifact kind goes. At least one entry. |
| `kinds` | What frontmatter each kind accepts. |
| `behaviors` | Constructs in a command body the tool treats differently from Claude Code. |
| `shared` | Fragments of checks to add. The only one is `skill-rules`: lola's findings for skills that run shell with ``!`…` `` or fill `$ARGUMENTS`, which tools reading a shared skills directory do not. |

## `layout` and mechanisms

`layout` maps an artifact kind (`skill`, `command`, `agent`, `mcp`, `rule`, `runtime-hook`) to an
entry. The entry's `mech` names the Go mechanism that writes it; the other fields are that
mechanism's parameters, and any it does not take is an error.

| `mech` | Places | Required | Optional |
|---|---|---|---|
| `skill-dir` | `skill` | `path` | `shared`, `reads_shared_skills`, `name_matches_dir`, `prepend_name`, `hook_settings` |
| `agentskills-dir` | `skill` | `path` | `shared` |
| `markdown-file` | `command`, `agent` | `path`, `ext` | `prepend_name`, `defaults`, `arguments`, `sidecar`, `hook_settings` |
| `toml-command` | `command` | `path`, `ext` | `sidecar` |
| `json-servers` | `mcp` | `file`, `pointer`, `shape` | none |
| `rules-dir` | `rule` | `path` | `concat`, `register` |
| `claude-hooks` | `runtime-hook` | `file` | none |

Parameters:

- `path`: a directory relative to the target root, clean and inside it. `ext` is a file extension
  with its dot (`.md`, `.toml`).
- `shared: true`: other tools install into `path` too, so `lola tidy` names what lola does not own
  there as theirs.
- `reads_shared_skills: true`: the tool also reads the `agents` target's skills directory, so lola
  does not write a second copy of a skill that target delivers.
- `name_matches_dir: true`: a skill's `name:` must equal its directory name.
- `prepend_name: true`: add `name:` from the file name when the frontmatter has none.
- `defaults` (agents): frontmatter keys added to an agent that lacks them (`{mode: subagent}`).
- `arguments` (commands): `keep` leaves `$ARGUMENTS`, `$0`, `$1` as written; `one-based` renumbers
  them from 1 and reports it.
- `sidecar` (commands, and required for them): `copy` copies a command's sidecar directory
  beside the command; `drop` does not. `markdown-file` and `toml-command` commands need one or the
  other.
- `file` (`json-servers`, `claude-hooks`, `register`): a path, or `{first: [a, b]}` for the first
  that exists, else the last. `first:` needs two or more candidates. `claude-hooks` takes a plain
  path only.
- `pointer` (`json-servers`): a JSON Pointer to the servers object (`/mcpServers`). `shape` is
  `claude`, `gemini` or `opencode`.
- `concat` (`rules-dir`): one file name; the module's rules are joined into
  `<path>/<module>/<concat>` instead of one `<path>/<module>/<rule>.md` each.
- `register` (`rules-dir`): after writing, add a member to the JSON list `list` (a JSON Pointer
  such as `/instructions`) in `file`. `member: dir` adds the module's directory; `member: glob`
  adds `<dir>/*.md`.

### Path variables

- Roots: `{project}` in `roots.project`; `{home}`, `{config_home}`, `{data_home}` in `roots.user`.
- Layout paths are relative to the root and may use only the era's own `vars`, written as `{name}`
  (`vars: {dir: {project: .mytool/, user: ""}}` then `path: "{dir}skills"`). A variable's value is
  per scope and cannot hold `{` or `}`. `project`, `home`, `config_home` and `data_home` are
  reserved names.
- After expansion every path must be clean, relative and free of `..`; otherwise the spec is
  refused.

## `kinds`: what frontmatter the tool accepts

For each kind the layout places:

```yaml
kinds:
  agent:
    unknown_keys: keep        # keep, or drop (the default) with a warning
    required: [description]
    name: '^[a-z0-9]+(-[a-z0-9]+)*$'   # names the tool accepts; omit to accept any
    keys:
      description: {accepts: {shape: string}}
      model: {accepts: {shape: string, pattern: '^[^/]+/.+$'}}
      name: {implied_by: file-name}
      hooks: {unsupported: mytool runs no hooks from frontmatter, case_insensitive: true}
```

Each key states exactly one fact:

- `accepts: {shape, pattern}`: `shape` is `string`, `string-list`, `bool-map`, `bool`, `number`,
  `positive-integer` or `map`. `pattern` (RE2) applies to `string` and `string-list` only.
- `implied_by: file-name`: the tool takes this from the file name, so lola leaves it out.
- `unsupported: <why>`: the tool ignores the key; the reason is shown in the finding.

Modifiers:

- `case_insensitive: true`: the key matches in any case.
- `absent_means: unrestricted`: without this key the artifact may do anything (a missing
  `tools:` means every tool), so lola must not quietly drop a restriction it cannot keep. It needs
  exactly one of `alternative: <key>` (the key the tool restricts with instead) or `example:
  <value>` (a value the tool accepts; only with `accepts: {shape: string-list}`).
- A key that is both `required` and `unsupported` is an error.

## `behaviors`: command bodies the tool reads differently

```yaml
behaviors:
  - {id: shell-fence, kind: command, detect: "(?m)^[ \\t]*`{3,}!", effect: sent-as-text,
     what: fenced shell, alternative: "inline !`…`"}
  - {id: shell-unchecked, kind: command, detect: "!`[^`]+`", effect: runs-without-permission-check,
     what: "!`…`"}
```

`id` is unique within the era, `kind` is `command`, `detect` is the pattern that finds the
construct and `what` names it as the tool would. `effect` is one of:

- `sent-as-text`: the tool passes the construct to the model as text. Give exactly one of
  `native:` (the tool's own syntax for it) or `alternative:` (a form it does run).
- `runs-without-permission-check`: the tool runs it with none of Claude Code's checks. Give no
  `native:` or `alternative:`.

The layout must place commands.

## Consent for a component's hooks and MCP servers

A skill, command or agent can carry `hooks` or `mcpServers` in its frontmatter, and the assistant
runs them. lola writes them only where you consent to running them, through the same question as a
module's runtime hooks. A spec can opt a kind in with `hook_settings`:

```yaml
vars:
  settings: {project: .mytool/settings.local.json, user: .mytool/settings.json}
layout:
  agent: {mech: markdown-file, path: .mytool/agents, ext: .md, hook_settings: "{settings}"}
  runtime-hook: {mech: claude-hooks, file: "{settings}"}
```

- `hook_settings` must equal the `file` of a `runtime-hook` entry using `claude-hooks`, at both
  scopes. Consent for the component's hooks is consent to write that one file.
- A kind with `hook_settings` is rendered as Claude Code reads it, so the mechanism refuses the
  parameters that would change that: `markdown-file` takes no `arguments`, `defaults` or
  `sidecar: drop`, nor `prepend_name` on a command (an agent keeps it), and `skill-dir` no
  `name_matches_dir` or `prepend_name`.
- At project scope only, lola checks that git ignores the settings file it writes. When git could
  commit it, the install skips that write with the finding `<id>/settings-local-not-ignored`
  (`--force` overrides). Add the file to `.gitignore`.

For every other layout entry, lola drops `hooks` and `mcpServers` (in any case) from a skill,
command or agent's frontmatter and warns, whatever the spec's `kinds` say. A spec cannot widen
this.

## Replacing a built-in

Give a spec a built-in's id and yours replaces it whole: nothing is merged. Start from a copy of
the built-in:

1. Copy its spec from
   [`internal/targetspec/specs/`](../../internal/targetspec/specs/) (`claude-code.yaml`,
   `gemini-cli.yaml`, `opencode.yaml`, `agents.yaml`) to `~/.config/lola/targets/<id>.yaml`.
2. Edit the copy.

lola tells you each time:

- On stderr, in human output only (never with `--json` or `--plain`): `note: opencode uses your
  target spec /home/you/.config/lola/targets/opencode.yaml in place of lola's built-in`.
- As an info finding `target/user-spec` on the assistant, in `lola check` and install plans.

A replaced built-in stops following lola. When a new release changes the built-in (a new frontmatter
key, a moved directory), your copy keeps the old behaviour. Compare it against the shipped spec
after upgrading, or delete your file to return to the built-in.

## Removing a spec you installed with

`lola uninstall` needs the target that wrote the files. If you delete the spec of a target you have
installed into, `lola uninstall` (and every other command that reads that scope's lock) refuses
with `target "<id>" is not a target lola knows`. Its first hint says to restore your target spec
for `<id>` in `~/.config/lola/targets` and run the command again: lola needs the spec to remove what
it wrote. The second is `lola repair`, which forgets those entries and leaves their files on disk.
Restore the spec, uninstall, then delete it.

## When a spec is wrong

A spec that cannot be loaded stops every lola command, whether or not it touches that target. The
error names the file, the field and the fix, for example:

```
error: /home/you/.config/lola/targets/quill.yaml: eras[0].layout.command.sidecar: must be copy or drop
  hint: say whether the tool gets a command's sidecar directory
  hint: lola stops rather than render with its built-in in place of your spec; fix or move aside /home/you/.config/lola/targets/quill.yaml
```

A spec holds plain text: a control or format character in any value (`"\e[31m"` and the like,
which YAML's double-quoted escapes produce) is refused, so a spec cannot send terminal codes through
lola's output. Patterns (`match`, `detect`, `pattern`, a kind's `name`) may also hold a tab or a
newline.

lola never falls back to a built-in or skips the file: you would otherwise install into the wrong
place without knowing it. Fix the field, or move the file out of the directory.
