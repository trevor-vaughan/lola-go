# `lola info`

```
lola info <source|@market/module|module> [-t <assistant>…] [--ref <ref>] [--path <dir>]
```

Aliases: `show`, `view`. `lola market <name> <entry>` is the same run as `lola info
@<name>/<entry>`. Show a module before you install it, running nothing: no install hook,
runtime hook or MCP server. The argument is what `lola install` takes: a module in your cache,
`@market/module`, or a source. A source or a marketplace entry is fetched into your cache as
`lola cache add` fetches it, so installing the cached name afterwards fetches nothing. It prints:

1. The module's identity: name, version, description, source, type, path, digest, format and
   cached heads. A git source also prints `commit:`, the full 40-hex commit it resolved, and
   `ref:`, the branch, tag or commit the version shown was read at (`default branch` when none);
   for a cached module named without `--ref` and installed in the scope, the ref its entry
   records, which `lola update` follows. `head digest:` is the version digest, never a commit (with several cached heads, each line
   names its ref). `--json` has `commit`, `""` for a folder or an archive, and `ref` (`""` for the
   default branch and for a source that is not git); `lola cache add` and `lola cache list` JSON
   carry `commit` too.
2. Its contents: skills, commands, agents, MCP servers, rules, install hooks, warnings, the
   [vars](../guides/templating.md#vars) it declares with their defaults, and its
   [templates](../guides/templating.md#templates), [partials](../guides/templating.md#partials) and
   [attribute files](../guides/templating.md#attribute-files). Under `mentions:`, each component
   whose text names others of the module (`skill/golang-race → skill/golang-lint`); a mention
   is information, never a dependency. If lola cannot read the module's files to find them, a
   warning says so and the list is empty.
3. What would run code, and when:
   - each native executable (ELF, Mach-O or PE) the module installs, under `ships native
     executables:`: its path, format, kind, architecture and OS, whether it links libraries
     statically or dynamically and which, the Go version and module of a Go program, its size
     and its sha256 (`none` when there are none). See [Native executables](../usage.md#native-executables).
   - each install hook with its sha256 (what consent is recorded for) and its first 5 lines,
     each cut at 200 characters;
   - the runtime hooks (hook sets, and hooks and agents' MCP servers in components' frontmatter)
     each assistant would run, as the consent question shows them;
   - each MCP server with the command line or URL the assistant would run, for each assistant
     that can express it.
   - Without a plan (no scope, no assistant, or a refusal) they are shown as the module declares
     them, `${CLAUDE_PLUGIN_ROOT}` and its kin unexpanded. Author text is shown on one line.
   - Then the install scripts that would wrap the module's install, in the order they run, each
     with its source and sha256 and whether it needs your trust (a project's): without a scope,
     only your own from `config.yaml`.
4. What installing it here would do, for each scope `lola install` would pick without asking:
   - the plan `lola install -n` prints (files per assistant, findings, `new project at <dir>`
     for a project without `.lola/`);
   - each consent the install would meet (`scripts`, `hooks`, `runtime-hooks`) and whether you
     gave it already, and the `--trust=` kinds still needed;
   - when installing there would be refused (an untrusted `project.yml` entry, a conflict, any
     refusal that coaches), that refusal with its hints instead, and no install; any other
     failure (an unreadable file) is `lola info`'s own error.
   - where a layer provides the module (it is in `project.yml`, your default or the profile on),
     each component as that layer's picks leave it, under `components of <module> here, provided
     by <layer>:` (the layer named as the component lines name theirs): `installed` (the selection takes it), `skipped in <layer>`, `not in the only: list of
     <layer>` or `rules off`, with `(new)` on one the version shown adds over the one installed
     there (nothing is new when that version is no longer in your cache), then
     `→ change it: <command>`: `lola profile <name> --edit <module>` for a profile's or your
     default's module, `lola install <module>` for `project.yml`'s while no profile is on there
     (`lola profile base --edit <module>` while one is), and for a remote profile's module the
     switch to a copy of yours (`lola profile go-mine --switch --new --from remote:go`).
     For a git module `project.yml` provides while no profile is on there, a second line
     `→ follow another branch or tag: lola install <module> --ref <ref>` (`<ref>` is yours to
     fill in) switches it to another ref, keeping its picks and targets.
   - A copy of the version already installed needs no `hooks` trust: its hooks run only with
     your consent.
5. The command that installs exactly the version shown, with `--trust=` the kinds still needed
   (none: no `--trust`), keeping your output flags, scope flags, `-C` and `-t`; there is none
   when installing at any scope would be refused, or when every scope already has that version
   installed as planned (installing it again would change nothing).
   - A git source, and a marketplace entry from git without a component selection, is named as
     its git source at the full commit SHA fetched, in place of a branch or tag you gave, since
     a branch can move before you install (`--path` as the source needs); a commit your cache
     holds installs without fetching.
   - An archive, a folder, a cached name and any other entry (one that selects components among
     them) are named by the cached name at the head shown (`--ref <ref>` for a head other than
     the default branch's), which installs from your cache without touching the source.
   - A cached git module without a component selection whose entry in the scope records another
     ref is named by its source at the commit shown instead, so that installing it pins that
     commit in the entry (to follow a branch again, install with `--ref <branch>`).
   - What stays open: if you fetch a cached head again yourself (`lola cache update`, or
     installing the source at a branch) or run `lola market --update` between `lola info` and
     the install, a hint by cached name installs the new head.

   ```
   → install it: lola install https://example.com/kit.git --ref 4f0c2a9e1b7d3c5a6e8f9012ab34cd56ef7890ab --project --trust=hooks
   ```

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Plan for these assistants, as `lola install -t` would (repeatable or comma-separated). |
| `--ref <ref>` | The git branch, tag or full commit SHA to fetch. |
| `--path <dir>` | The module root inside the source (monorepos); refused with `@market/module`. |

Scope: `--scope` (`both` shows one plan per scope), `--user`, `--project` or `-C`; else the
project with `.lola/` at or above the current directory; else `default_scope` in `config.yaml`.
`lola info` never asks for a scope, on a terminal or off it, and never guesses one. With none,
it shows sections 1 to 3 and the consent it knows without a scope (`runtime-hooks` are needed
but their record is unknown, as it is kept per settings file), says why, and hints the runs that
name a scope, exiting 0:

```
no scope: no .lola/ was found in /work/x or above it, and config.yaml sets no default_scope
  hint: lola info ./kit --project
  hint: lola info ./kit --user
```

`--project` is not hinted in your home directory, where the user scope installs. Without `-t` and
with no assistant set up in the scope, it says which assistants are installed and hints the run
with `-t` and each of them (`lola info ./kit --project -t claude-code`); with none installed it
says so and hints nothing.

`--json` adds `native_executables`, always an array, each entry with `path`, `format` (`ELF`,
`Mach-O` or `PE`), `kind`, `arch` (an array), `os`, `malformed`, `size`, `libraries` (an array),
`linkage` (`static`, `dynamic` or empty), `go` (`go_version`, `module` and `module_version`, or
`null` for a program that is not Go) and `sha256`. `--plain` prints one `native` row each: path,
format, kind, architectures joined by commas, OS, linkage, sha256.

Refusals: no argument: `lola info needs a module or source`, hinting `cached modules: lola cache
list`; an argument that is neither: `"./nope" is neither a module in your cache nor a source;
sources are …`, hinting `cached modules: lola cache list`; `--path` with `@market/module`; and a
fetch failure, as `lola cache add`'s. A source whose module name your cache already holds from another
source is read without being cached, so info shows a fork beside the module it forks; installing it
is what the cache refuses.

JSON: one document. The module's identity and contents: `name`, `description`, `version`,
`digest`, `format`, `ref`, `source`, `type`, `heads` (each `ref`, `digest`), `skills`, `commands`,
`agents`, `mcp_servers`, `rules`, `hooks`, `warnings`, `vars` (`[{"name", "type", "default"}]`,
`type` one of `string`, `boolean`, `number`, `list`; a number's `default` is a JSON number),
`templates`, `partials` and `attributes` (paths in the module), each `[]` when none, and `path`
(the module root inside the source) when it has one. Then `mentions`: each component that names
others in its text (`component`, `mentions`, both `kind/name`; `[]` when none), and `notices`
(each `level`, `text`, `class`; `[]` when none), a warn when the scan could not read the module. Then `runs`: `install_hooks` (each `event`,
`script`, `sha256`, `first_lines`), `runtime_hooks` (as `lola install` reports them) and
`mcp_servers` (each `name`, `target`, `command`, `args`, `url`) and `scripts` (each `event`,
`source`, `path`, `sha256`, `needs_trust`). Then `install`, one element per
scope: `scope` (`""` without one), `targets`, `steps`, `findings`, `consent` (each `kind`, `item`,
`recorded`: `yes`, `no` or `unknown`), `trust_needed` (kinds), `digest` (the version it
installs), `command` (the next step; `""` without a plan, and in every element when installing at
any scope would be refused) and, when installing there would be
refused, `refusal` (`error`, `hints`), and `new_project` (`true` when the install would create `.lola/`
at the scope's directory), `from` (the layer that provides the module there, as `lock.json` names
it; `""` where none does, and where its scope or the cache cannot be read), `components` (each `kind`, `name`, `state`: `installed`, `skipped`,
`not_in_only` or `rules_off`, `origin`: the layer of the picks that leave it out, `""` otherwise,
`new` and `mentions`; `[]` where no layer provides the module, or its scope or the cache cannot be
read) `change` (the re-pick; `""` in those cases too) and `follow` (the switch to another ref; `""` for a module that is not git, and where a profile provides it). Under `--json` an unreadable scope or
cache gives `from: ""`, `components: []` and `change: ""` and no warning: the warning shows only in
human output. A runtime hook shown as declared has `status` `unknown`. Last,
`no_scope` (the sentence, only when no scope resolved), `no_assistant` (the sentence, only when a
scope lacks an assistant to plan for) and `hints` (`[]` when both a scope and an assistant
resolved).

`--plain` rows are the module's, then `mention` (component, mentioned), `notice` (level, text),
`hook` (event, script, sha256), `script` (event, source, path, sha256, needs_trust),
`runtime-hook` (as `lola install --plain` prints them), `mcp` (name, assistant, command line,
URL), `refusal` (scope, error) each followed by its `hint` rows, `consent`, `from` (scope,
layer), `component` (scope, kind/name, state, origin, new), `change` (scope, command), `follow` (scope, command), the steps,
`install`, `no_scope` or `no_assistant` (the sentence), and `hint` rows.
