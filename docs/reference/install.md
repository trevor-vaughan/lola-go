# `lola install`

```
lola install [<source|name|@market/module>...]
```

Alias: `add`. Fetch the module (if it is a source or a
[marketplace](../guides/marketplaces.md#marketplaces) entry), render it for each target, record it in
the file it writes ([Where it is written](#where-an-install-is-written)), and copy what it installs
into your cache.

Where a higher layer lists the module (the scope's active
[profile](../guides/profiles.md#profiles) over its base), that layer's entry keeps winning: the
install writes its entry and installs nothing of it, and when the entries differ it warns
`<module>: profile go's entry wins; this change to default (base) applies when it is off`. A
run that is cancelled, refused or `-n` leaves your cache as it was.

With no argument on a terminal,
walks you through the choices
([Choosing what to install](../guides/choosing-what-to-install.md)). A name installs a module already in your cache without
touching its source. Without a scope flag it uses, in order, the terminal's scope picker, the
project with `.lola/` at or above the current directory, then `default_scope` in `config.yaml`
([Choosing a scope](../guides/choosing-what-to-install.md#choosing-a-scope)).

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Install for these assistants (repeatable or comma-separated: `claude-code`, `gemini-cli`, `opencode`, and `agents`, which writes skills only into `.agents/skills`: see [Skills for every tool](../guides/choosing-what-to-install.md#skills-for-every-tool-the-agents-target)). Naming fewer than a module is installed on removes the others' files in the same run, after asking as a pick change does (`✗ my-module: gemini-cli (target dropped by -t)`). |
| `--ref <ref>` | The git branch, tag or full commit SHA to fetch. Running install again with another `--ref` switches an installed git module to it in place, keeping its picks and targets. |
| `--path <dir>` | The module root inside the source (monorepos). |
| `--at <module>=<version>` | Install the module only at this version: a git module's commit (any case), or a folder's or archive's `sha256:<digest>` (the digest lola prints). Repeatable; given twice alike it counts once; a module the install does not include is refused. If its source now serves another version, the install is refused, writing nothing, hinting `see what it serves now:` with `-n`. A refusal for a module's pre-install hook or the project's install script adds `--at` for the version it showed to its `then consent to it:` hint, so consent covers that version alone: a folder edited after you reviewed it is refused, its hook not run. |
| `--profile <name>` | Write the entry into this profile instead of the profile on; `base` is the scope's base (`default` too at user scope). A profile that is off is saved and not applied. |
| `--override` | When the module comes from another layer, give the target its own entry, starting from that one. Keeps the version installed (a folder module, never pinned, installs the version your cache holds). |
| `--skip`, `--only`, `--unskip <selector>` | Each repeatable or comma-separated (`--only skill/a,skill/b`). Skip, keep only, or install again components (`kind/name` or `module:kind/name`), and save the choice in the profile entry the install writes, or, for `project.yml`, in `selections.yaml` ([Component flags](../guides/choosing-what-to-install.md#component-flags)). |
| `--global` | Save `--skip`, `--only` and `--unskip` in the global section of `selections.yaml`. |
| `--exact` | With `--only`: save the selectors as an `only:` list, so components a later version adds are not installed; lola names them ([Component flags](../guides/choosing-what-to-install.md#component-flags)). |
| `--with-mentions` | Also install the components your picks mention by name, one hop, saved where the picks are ([Mentions](../guides/choosing-what-to-install.md#mentions)). |
| `--no-rules`, `--rules` | Leave the module's always-on rules out, remembered in the manifest; `--rules` puts them back. |
| `--strict` | Fail, writing nothing, if any target would drop, change or skip part of a module, or an installed assistant is of another era than the version declared (`version/era-mismatch`). A version you choose that differs from the installed one is warned of, not refused (`version/chosen-era`). |
| `-v`, `--verbose` | Print every notice in full, grouped, instead of one line per module for those that are not about safety ([Output contract](../usage.md#output-contract)). |
| `--pre-install <path>`, `--post-install <path>` | Your own script for this run, which wraps every module copy the install writes, once per assistant, around the module's own hooks; a relative path is taken from the directory you run lola in, and the script runs from that absolute path, never a file of its name in the module copy ([Install scripts](../guides/choosing-what-to-install.md#install-scripts)). No trust is asked. |
| `--trust=<kinds>` | Answer yes, without asking, to lola's questions before it runs code you did not write, of the kinds named, recorded as a yes at the prompt is: `scripts` (the project's install scripts), `hooks` (a module's install hooks), `runtime-hooks` (a module's Claude Code runtime hooks and agent MCP servers), or `all` for the three ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)). |
| `--force` | Overwrite unowned or hand-edited files and render skipped artifacts that can be, and apply without the review question ([Safety rules](../usage.md#safety-rules)); it answers the question about files a pick change or a narrower `-t` deletes too. |
| `-n`, `--dry-run` | Print the plan and write nothing, not even `selections.yaml`. |
| `-y`, `--yes` | Apply without the review question: the summary is printed, and the install goes on. Also confirms the files a pick change or a narrower `-t` deletes. |

## Where an install is written

An install writes one file per scope, the first of these that applies:

1. `--profile <name>`: that profile (`base` is the scope's base; `default` names it too at user
   scope and is refused in a project).
2. The profile on, when it is yours or the project's.
3. While a remote profile is on: the base, once you say so. A terminal asks; off one the install
   refuses, hinting `--profile base`.
4. The scope's base: `.lola/project.yml` in a project, your default profile
   (`$XDG_CONFIG_HOME/lola/profiles/default.yml`) at user scope.

A `--profile` that is off is written and nothing is applied; the notice says so (`saved to profile
rust; it is not on at user scope → lola profile --user rust --switch`). Its move hints name that
profile as the move's source (`lola profile --user rust --move kit --to base`), and a move into a
profile that is off keeps it off (`--keep-off`); nothing of the module is installed here to move.
After a write to a project's profile that is off, no move is hinted.

A module the target's file
does not list but another layer does (the base under the profile on, or a remote profile as a member
of the bundle on) asks whether to change it in that layer (`--profile base`) or to override it in
the target (`--override`; under a bundle, `--profile <member> --override`). When the profile on is a
bundle, an install asks which member gets the module, and off a terminal refuses with a
`--profile <member>` per member.

A read-only layer offers Override alone, and a request equal to the inherited
entry is a notice. `-y` and `--force` answer neither: off a terminal the install refuses, hinting
both flag forms. An override copies the entry without its pin and keeps the version installed, and
an install that changes only picks keeps the installed version too; `lola update` moves it.

A
folder module is never pinned: it installs the version your cache holds, as a sync of a cached
folder does. An
inherited entry that is pinned installs at its own ref and pin.

lola writes a profile of yours (`default.yml` included) with no `commit:` or `digest:`: the version
installed is in `lock.json`. A project profile's entries are written with their pins and recorded
as trusted, as `project.yml`'s are. An entry that is a marketplace address (`@market/module`) in
`project.yml` or `default.yml` is replaced by `lola install @market/module`.

Each entry written prints a line naming the file:

```
✓ added mod1 to profile go  (/home/me/.config/lola/profiles/go.yml)
✓ added mod1 to default (base)  (/home/me/.config/lola/profiles/default.yml, new)
✓ changed mod1 in project.yml (base, shared: git tracks it)
```

`added`, `changed` or `removed`; a file of yours shows its path (`new` when the run created it); the
base or a project profile says `shared: git tracks it` when it does; an entry with an `only:`
list adds `only: 4 skills`. Under `-n` the lines read
`→ would add …`. [`edits`](../usage.md#output-contract) in JSON and `edit` rows under `--plain` carry them.
`lola sync` and `lola update` report the re-pins they write into `project.yml` or a project
profile the same way.

An install that adds a folder or local archive inside a project (at or below a directory holding
`.lola/`, symbolic links followed; never your home directory) to a file of yours (`default.yml`, a
profile of yours) warns once, at that write: `notes (<file>) installs from <path>, inside project
<root>: changes there install without review wherever that file is on`. lola never asks about your
own entries, so a later change there (a pulled commit) installs on the next sync of every scope
that file is on in.

After a
user-scope install the warning hints the two-step that keeps it in that project instead (`keep it
in the project instead: lola uninstall --user notes, then lola install -C <root> notes`, with
`-t` when the project names no assistants you trust there), when that runs as printed. A later
sync says nothing of it, and neither does an entry you wrote by hand.

An interactive install names the file in each component page's title
(`profile go › review-kit › skills (2)`; `default (base) › …` for your default profile,
`project.yml › …` for the project's). With `--profile`, the question about a module another layer
gives comes before the page; without it, a page whose file a later question decides (under a
bundle or a remote profile, or for such a module) leaves the file out of its title. Answering
Override with the picks unchanged writes nothing and says so (`→ nothing changed: …`). Its review
shows a re-pin as `→ review-kit: re-pinned at 1a2b3c4`, and the equivalent commands carry the
answers to the questions above (`--profile base`, `--override`).

Refusals:

- No argument off a terminal: `lola install needs a module or source`.
- No scope found: `lola install needs a scope: no .lola/ project was found in <dir> or above it,
  and <config.yaml> sets no default_scope (default_scope: project, user or both)`, with one hint
  per scope (`lola install ./my-module --target claude-code --project`, then `--user`, then
  `--scope both` in its place).
- An argument that is neither: `"./nope" is neither a module in your cache nor a source; sources
  are …`, hinting `cached modules: lola cache list`.
- No `-t` and no assistant set up in the project: `none of your installed assistants
  (claude-code, opencode) is set up here, so lola does not know where to install`, with one hint
  per assistant (`lola install ./my-module --project -t claude-code`), the last being `-t agents`. An unknown one: `unknown
  target "nosuch"`.
- A module another layer provides, off a terminal (or with `-y`/`--force`): `mod1 comes from
  default (base); lola install needs to know whether to change it there or override it in profile
  go, and there is no terminal to ask on`, hinting the run with `--profile base` and with
  `--override`.
- A pick change or a narrower `-t` that deletes installed files, off a terminal: `lola install
  needs your confirmation to remove files, and there is no terminal to ask on; nothing was
  written`, after the files (`✗ my-module: gemini-cli (target dropped by -t)`), hinting the same
  run with `-y`.
- An unknown `--profile`: `no profile nosuch for user scope`, hinting the profiles the scope can
  use. A `selections.yaml` with a `user:` section: `user: is no longer used; picks for user-scope
  modules live in their profile entries`, hinting `delete the user: section from <path>`, then
  `lola install <module> --user --skip <kind/name>` to re-apply a pick.
- A cached version whose content does not match the digest an entry pins: `cached version
  my-module/1a2b3c4d does not match its digest sha256:…`, hinting `remove it from your cache, then
  install it again from its source: lola cache rm my-module`.
- A conflict ([Safety rules](../usage.md#safety-rules)), `--strict` with a finding (`--strict: 1
  finding(s); nothing was changed`), and a project whose `.lola/lock.json` git tracks.
- An install script that is missing, not a regular file or not executable, and a project's that
  is an absolute path, resolves outside the project or is a symbolic link, before anything is asked
  ([Install scripts](../guides/choosing-what-to-install.md#install-scripts)); the project's pre-install script without your trust off a
  terminal.

JSON: `modules`, `steps`, `findings`, `notices`, `edits`, `scripts`, `dry_run`, `scopes`,
`target_versions` and `equivalent_commands` ([Output contract](../usage.md#output-contract)).
