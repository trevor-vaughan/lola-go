# `lola profile`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola profile                         (lola profile list, ls)
lola profile <name>                  (lola profile show <name>)
lola profile on <name>
lola profile off [<name>]
lola profile switch <name> [--new [--from <profile> | --bundle <a>,<b>...]]
lola profile edit [<name>] [<module>...] [--only|--skip|--unskip <kind/name>] [--exact] [--override] [--new [--from <profile> | --bundle <a>,<b>...]]
lola profile edit <name> --bundle <a>,<b>...
lola profile move <module>... --to <name> [--from <layer>] [--switch|--keep-off]
lola profile split <name> [--to <name>]
lola profile add <source> [<name>] [--ref <ref>] [--path <dir>]
lola profile update <name>... | --all
lola profile rm <name> [--force]
```

Bare, or as `lola profile list`, list the [profiles](../guides/profiles.md#profiles) each scope can
use (yours, and at project scope the project's): kind, state (`base` for `default` at user scope,
`on`, `in bundle <the one on>` for a profile or nested bundle the bundle on reaches, `off`;
`(bundle)` follows `on` and `off` for a bundle) and its file. `--json` gives `state`
(`base|on|member|off`), `bundle` and, for a bundle, its `members`. A file that does not load, or a
bundle that does not expand, is listed all the same, with a notice.

`lola profile <name>`, or `lola profile show <name>`, shows the profile and, per scope, the layer each of
its modules comes from, the layers that entry overrides, and its picks (`only:` and `skip:` lines
under it; a `skip:` list of 10 or more that is the complement of fewer installed components, every
skip naming one component of the installed version, shows as `only (not exact): <picks>; <n> others
skipped`, which is what a non-exact `--only` saves; `--plain` adds them as two last columns, comma-separated; JSON `only` and `skip` arrays
per module); for a profile that is off, what
switching to it would give.

Listing and showing never ask for a scope: without a flag they show the
project found from here, if any, and user scope. `on`, `off`, `switch`, `rm`, `move` and `split` take `--project`,
`--user`, `-C <dir>` or `--scope` (before or after the subcommand); without one, a terminal asks (nothing preselected), else the
project with `.lola/` at or above the current directory, else `default_scope`, else the run is
refused. `default_scope` does not apply to listing and showing. One profile is on in a scope
at a time, over the scope's base (`default` at user scope, `project.yml` in a
project). Every `lola profile` run refuses a project whose `.lola/lock.json` git tracks.

| Subcommand | What it does |
|------------|--------------|
| `list` | List the profiles, as bare `lola profile` does. Alias: `ls`. |
| `show <name>` | Show a profile, as `lola profile <name>` does. |
| `on <name>` | Turn the named profile on, when none is. With another on, a terminal asks whether to switch; off a terminal it refuses, hinting `switch`. `lola profile on base` with no profile on is only a notice (the base is always loaded); with one on it is refused. Aliases: `enable`, `activate`. |
| `off [<name>]` | Turn off the profile on (name it, or give no name) and apply; asks when files go (`-y`). `off base` is refused: the base is always loaded. Aliases: `disable`, `deactivate`. |
| `switch <name>` | Make the named profile the one on, whatever is on, and apply; never asks which. `lola profile switch base` (at user scope also `switch default`) turns the profile on off. `--new` makes it first. Alias: `use`. |
| `edit [<name>] [<module>...]` | Edit the named profile, else the one edits go to (the profile on; the base), as `lola install --profile <name>` would write it: the modules named, their picks with `--only` (skips of the rest; with `--exact` the entry's `only:` list), `--skip` and `--unskip`, applied where it is on and saved where it is off. Asks no scope (the scope flag's, else the project found here, else user scope); a profile of yours that is on in the other scope too is applied there as well, each scope with its own context line and result. A module another layer gives asks whether to change it there (`lola profile edit base …`) or give the profile its own entry (`--override`); on a terminal it asks before the module's component page, as does the question of which member of the bundle on gets it, or whether to write the base under a remote profile, so the page is titled with the file the answers write (`profile go-review › cc-skills-golang › skills (7)`). The first name is the profile when one has it and no module does, `base`/`default`, a qualified name, or any name with `--new`; otherwise every name is a module. A name that is both a profile and a module asks on a terminal and is refused off one, hinting each qualified. On a terminal without a module it opens a page: the profile's modules ticked, then each one's components (a module you unticked is removed once asked), a bundle's members, or for a new name whether it `Lists modules` or `Bundles profiles`. Aliases: `pick`, `modify`. See [Editing a profile](../guides/profiles.md#editing-a-profile). |
| `move <module>...` | Move whole entries, in one transaction, from the layer each comes from (or `--from`'s: a profile, `base`, `default` or `project.yml`) to `--to`, keeping the version installed and the picks (a module leaving `project.yml` takes its `selections.yaml` picks into the profile entry: `picks: carried from selections.yaml`; picks a profile entry cannot hold, such as an `include:` over a global or glob skip, refuse the move unless `--force`), and apply. Alias: `mv`. Across scopes it prints each scope's context line: the target's, where it writes, then the source's once lola knows the entry comes from there. See [Moving a module](../guides/profiles.md#moving-a-module). |
| `split <name>` | Rewrite a profile with both `bundle:` and `modules:` into a bundle of its members then `<name>-modules` (or `--to`'s name), which gets the modules and targets. Asks on a terminal (`-y`, `--force`); `-n` prints the plan. |
| `add <source> [<name>]` | Fetch a [remote profile](../guides/remote-profiles.md) from a git repository or an https archive, show it and ask before keeping it. Every entry of a remote profile must be pinned. Alias: `get`. |
| `update <name>...` | Fetch the named remote profiles, or `--all`, again, show what changed, ask, and apply them where they are on. Alias: `upgrade`. |
| `rm <name>` | Delete a profile of yours (`user:<name>`, or a bare name only yours has; its file goes to lola's trash) or a remote one; asks (`-y`), and is refused while it is in use (on, in the bundle on, or in a bundle the one on nests) unless `--force`. A bare name both have is refused, naming both; a project's profile is committed (`git rm` it); the base is never removed. Aliases: `remove`, `delete`, `del`, `erase`. |

| Flag | Subcommands | What it does |
|------|-------------|--------------|
| `--new` | `switch`, `edit` | Make the profile first, in the same run: yours (`project:<name>` makes the project's), empty unless `--from`, `--bundle` or, with `edit`, modules. With `edit` nothing is switched on. See [Bundles](../guides/profiles.md#bundles). Alias: `--create`. |
| `--from <profile>` | `switch`, `edit` | With `--new`: copy that profile's entries and targets (a bundle's members too), never a pin. `project:<name>` copies into yours only what a `move` would carry. |
| `--from <layer>` | `move` | The layer the entries leave: a profile, `base`, `default` or `project.yml`; without it, each module's own. |
| `--bundle <a>,<b>` | `switch`, `edit` | With `--new`: make a bundle of these profiles, in order (comma-separated). A bare member that does not exist is made empty; a `project:` or `remote:` one must exist. With `edit` alone: set a bundle's members, in order (`''` for none), making a bare one that names no profile, and apply them where the bundle is on, or nested in the bundle on. A project's bundle is edited in place; a member list it pulled and you have not trusted is asked about first (`--trust=profiles` answers; `-y` and `--force` never do). |
| `--ref <ref>` | `add`, `edit` | The branch, tag or commit to fetch. |
| `--path <dir>` | `add`, `edit` | For `add`: the directory of the source that holds `profiles/`. For `edit`: the module root inside the source. |
| `--skip`, `--only`, `--unskip`, `--exact`, `--with-mentions`, `--override`, `-t` | `edit` | As `lola install`'s (repeatable or comma-separated), saved in the profile's entry: `--only` skips the rest, so components a later version adds install; `--only --exact` writes its `only:` list, so they do not; and `--exact` without `--only` is refused. `--override` copies another layer's entry into the profile; it is refused for a project's entry its own sync has not trusted, and for one whose folder lies inside a project, as `lola install --override` is. |
| `--to <name>` | `move`, `split` | For `move`: a profile of yours or the project's, or `base`; a new name creates your profile. For `split`: the new profile's name (default `<name>-modules`). |
| `--switch`, `--keep-off` | `move` | Into a profile that is off here: `--switch` turns it on; `--keep-off` keeps it off, so the modules' files are removed here (asked; `-y`, `--force`). |
| `--all` | `update` | Every remote profile you have. |
| `--trust[=<kinds>]` | `on`, `off`, `switch`, `edit`, `move`, `split`, `add`, `update` | Answer yes, without asking, to the questions of the kinds named ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)): `entries` and `profiles` (the changes to `project.yml` and its profiles the run installs, a project profile taking your profile's place, a remote profile `add` or `update` shows), `scripts` (the project's install scripts), `hooks` (a module's install hooks), `runtime-hooks` (a module's Claude Code runtime hooks and agent MCP servers), or `all`, recorded as a yes at the prompt is. `add` takes `profiles` only (bare `--trust` there is `profiles`); `update` without a scope flag applies only `profiles` to the update and the other kinds to each scope's sync. |
| `--unseen` | `on`, `off`, `switch`, `edit`, `move`, `split`, `update` | Let `--trust`'s `scripts`, `hooks` and `runtime-hooks` consent to content lola has not shown you too; without it they consent only to what an earlier output showed you in full ([What lola has shown you](../guides/install-trust-kinds.md#what-lola-has-shown-you)). For CI and fresh machines. Refused without one of those kinds in `--trust`; `--force` never implies it. |
| `-y`, `--yes` | `on`, `off`, `switch`, `edit`, `move`, `split`, `update`, `rm` | Open no scope picker (the scope is the one found off a terminal, or the run is refused as it is there), confirm removals, including `rm`'s; it never trusts changes, nor answers `on`'s question. |
| `--force` | `on`, `off`, `switch`, `edit`, `move`, `split`, `rm` | Remove files without asking, edited ones too. For `rm`: remove a profile in use, without asking. Never a consent question, nor `on`'s question. |
| `-n`, `--dry-run` | every subcommand that changes something | Print what the run would change, ask nothing and write nothing. |
| `-v`, `--verbose` | `on`, `off`, `switch`, `edit`, `move`, `split`, `update` | Print every notice in full instead of one line per module for those that are not about safety ([Output contract](../usage.md#output-contract)). |

Each subcommand takes only its own flags. A flag that another subcommand takes is refused, naming the
subcommands that take it, with each one's usage (`--to applies to lola profile move and split`). An
old action flag is refused with the subcommand that runs instead, keeping the run's names and flags:
`lola profile go --on --user` hints `lola profile on go --user`.

Names and subcommands:

- A subcommand wins over a profile of its name: `lola profile off` runs `off`, and when a profile
  `off` exists it adds the notice `ran off; to show the profile named off: lola profile show off`.
  Every subcommand and alias adds it, on stderr (a refused run too) and under `--json` in the
  document's `notices` (a refused run's error document carries none).
- A word that is neither a subcommand nor a profile, within two edits of a subcommand, is refused
  (`unknown profile or command "swtich"`), hinting `did you mean: lola profile switch …` with the
  run's names and flags, and the listing.
- Making a profile (`switch --new`, `edit --new`, `split --to`, `add <source> <name>`) refuses a name
  that is a subcommand or alias of `lola profile` (`use is a lola profile command, so no profile can
  be named use`), hinting the run with another name. A file of that name made by hand still loads.

Refusals:

- An unknown name: `no profile absent for user scope` (or `for the project at <dir>`), hinting the
  run that makes it (`make it: lola profile switch absent --new --user`, with the run's scope and
  output flags, never `-y`) and the profiles the scope can use, or where to write one.
- `show`, `on`, `switch`, `split` or `rm` without a name: `lola profile on needs a profile`, hinting
  its usage and `find one: lola profile list` with the run's scope and output flags (`off` without a
  name turns off the profile on).
- Two names for `rm`: `rm deletes one profile; name it`, hinting the run with each.
- An old action flag (`--on`, `--off`, `--switch`, `--edit`, `--move <modules>`, `--split`, `--add
  <source>`, `--update`, `--remove` and their aliases), before or after the other flags:
  `--on is not a flag of lola profile`, hinting the subcommand with the flag's value first, then
  every word you typed, in order, and the flags you gave that it takes, each letter of a cluster
  such as `-ny` on its own (`lola profile go -n --on` hints `lola profile on go -n`). `lola profile
  <layer> --move a,b --to me` hints `lola profile move a b --from <layer> --to me`. When the
  subcommand would refuse those names, the hint is its usage and `find one: lola profile list`.
  `lola profile uninstall go` is refused as `uninstall is not a lola profile command; it is lola
  profile rm`, hinting `lola profile rm go`.
- Two names for `show`: `name one profile to show`, hinting the listing and each one shown.
- Two names for `switch` or `on`: `one profile is on at a time; to combine go and docs make
  a bundle`, hinting the bundle made and switched to (`lola profile switch go-docs --new --bundle
  go,docs`), with the run's `--trust`, `-n` and output flags, never `-y` or `--force`. With `off`:
  `one profile is on at a time; off takes one name`, hinting the run with each.
- `on` while another profile is on, off a terminal (or under `-n`): `user:go is on, and lola profile
  on does not replace it without asking; switch does`, hinting:
  - `lola profile switch user:docs` with the scope's flags and the bundle of both (`lola profile
    switch go-docs --new --bundle go,docs`);
  - and, when the profile on is a bundle of yours or the project's that can take another member and
    has no untrusted pulled member list, adding it to that bundle (`or add it to bundle work: lola
    profile edit work --bundle go,docs`).
  - On a terminal the question has up to four answers: switch to the named profile, make the bundle
    of both and switch to it, add it to the bundle on (which stays on; offered under the same
    conditions, and the run's own `--trust` applies to the edit), or cancel; `-y` and `--force`
    answer none.
- `--new` on another subcommand than `switch` or `edit`: `--new applies to lola profile switch and edit`.
  - `--from` or `--bundle` without `--new`: `--from makes a new profile and goes with --new`,
    hinting the switch to the profile that exists.
  - `--from` with `--bundle`: `--from copies a profile and --bundle combines several; pick one`,
    hinting each alone.
  - `--new` for a name that exists: `docs names user:docs (<file>); --new makes a profile that
    does not exist yet`, hinting the switch and a free name; for `project:docs` beside `user:docs`
    (or the reverse) it adds that a bare `docs` would then name two profiles, and the free name
    keeps the kind (`project:docs-2`).
  - A name that is `base` or `default`: `base is reserved for the scope's base; name the profile
    something else`.
  - A `--bundle` member that is no profile you have, or a `project:` one that does not exist,
    refuses with a hint that makes it first.
- `edit` off a terminal without a module: `lola profile edit edits profile go by its modules:
  name one, as in lola profile edit go <module> --only <kind/name>`.
  - A name no profile has: `no profile try for user scope; --new makes it`, hinting `lola profile
    edit try --new`.
  - `--new` without a name (`name it, as in lola profile edit <name> --new`); `--from` without
    `--new`; `--from` with `--bundle`; `--bundle` or `--from` beside modules; `edit --bundle`
    without a name (`name the bundle to edit`), of a
    profile that lists modules (`go lists modules; a bundle is a file of its own`), of the base, of
    a remote profile, or of a file with `targets:` beside the bundle (`<path>:<line>: targets: a
    bundle has none`): each hints a run that works.
  - A name that is both a profile and a module, off a terminal: refused, naming both, hinting each
    qualified.
  - A module another layer gives, off a terminal: refused, hinting `lola profile edit base …`
    and `--override`.
  - `--override` of a project's entry its own sync has not trusted, or whose folder lies inside a
    project, is refused as `lola install --override` is.
  - An edit of a project bundle whose pulled member list you have not trusted asks the trust
    question first, showing what was pulled and every other change the run would ask about (off a
    terminal it refuses, hinting the same run with `-n`, which prints them, and with
    `--trust=profiles` (and the other kinds shown), which accepts exactly those; `-y` and
    `--force` never answer it).
- `rm` of:
  - a bare name both a profile of yours and a remote one have (`go names your profile user:go and
    the remote profile remote:go`), hinting each qualified;
  - a project's profile (it is committed: `git rm` it);
  - the base;
  - a name nothing has (`you have no profile x and no remote profile x`).
  - While the profile is in use: `user:go is on at user scope`, hinting the `off` that frees it,
    then `or remove it anyway: … --force` (never `-y`, which would then be refused).
  - `--force` of a profile that a bundle of yours lists leaves that bundle refusing to load; the
    warning prints, for each such bundle, the run that takes the member out.
- `split` of:
  - A file without both keys: `<file> does not have both bundle: and modules:; there is nothing to
    split`.
  - A remote profile: `read-only; lola does not edit remote profiles, and none has a bundle: key`,
    hinting a copy of yours.
  - A split that would trust a project bundle's member list refuses off a terminal (`lola profile
    needs your trust …`), hinting `split -n` to see it and `split --trust=profiles -y`.
  - Any command that reads a profile with both keys refuses with its line and `lola profile split
    <name>`; on a terminal it offers the split (`Split work now?`) and then runs again.
- A bundle with a cycle, a member that is no profile, more than 8 levels, or more than 64 profiles
  or 64 bundles after expansion refuses with the `path:line` of the member that crosses; a cycle
  has no hint.
- `--profile` or `--to` naming a bundle: `work is a bundle; it lists profiles, not modules` (an
  install) and `work is a bundle; move into one of its members` (a move), hinting each member.
- A file with `extends:`: `extends: is no longer a key; a bundle combines profiles now`, hinting
  the edit that keeps what it did (replace the key with `bundle: [remote:go]`; with `modules:`
  or `targets:`, the `split` after it).
- `lola uninstall` of a module the base gives, while a bundle is on, asks whether to remove it
  there, as under any profile; off a terminal it hints `--profile base -y`, and `-y` alone answers
  when the base is the one layer that lists it.
- `on base` while a profile is on: `the base is always loaded; user:go is on over it`, hinting
  `lola profile switch base`; `off base`: `the base is always loaded; lola profile off turns
  off the profile over it`; `default` at project scope: `default is user scope's base; this
  project's base is project.yml`.
- `rm` of a profile you do not have: `you have no remote profile x`.
- `update` with names and `--all`, or with neither: `--all updates every remote profile you have,
  and takes no names`; `update needs the remote profiles to update, or --all; you have …`.
- `move` without `--to`: `lola profile move needs --to, the profile the entries move to`, hinting
  each `--to` the move takes as things stand.
- `move` to a profile that is off, off a terminal, with neither flag: `me is not on at user
  scope; lola needs to know whether to switch to it, and there is no terminal to ask on`, hinting
  the run with `--switch` and with `--keep-off -y`. `--switch` with `--keep-off`: `--switch and
  --keep-off cannot be combined; pick one`, hinting each alone. Both flags are refused with
  `--to base`, and `--keep-off` with a target that is on.
- A module both scopes list, when the run names both (`--user -C <dir>`): `notes is in default
  (base) at user scope and in project.yml (base) in this project; name the layer it moves from`,
  hinting the run with each `--from`.
- `move` of a module whose entry already comes from the target: `kit is in default (base)
  already`.
  - A target that lists the module while its entry comes from another layer asks to replace that
    entry instead (`-y` or `--force` answer it; off a terminal: `profile me lists kit already;
    lola needs to know whether to replace its entry, and there is no terminal to ask on`, hinting
    the run with `-y`).
  - Of a module no layer lists: `kit: no layer at user scope lists it`.
  - Into `project.yml` from user scope: `project.yml is a project's base; lola profile move never
    moves an entry into it from user scope`.
  - Into or out of a remote profile: refused, it is read-only.
- A move out of a project, or from `project.yml` or a project profile into a profile of yours, of
  an entry the project did not install as you trusted it (and an `--override` of `lola install`
  or `lola profile edit` that would copy one there):
  - `extra is not installed in this project as its files list it now; lola copies or moves out of
    a project only what it installed as you trusted it`, hinting `lola sync --project extra`.
  - From a project profile that is off: `extra is in profile team, which is off in this project;
    …`, hinting `lola profile --project switch team`.
- A move from `project.yml` or a project profile into a file of yours (a profile of yours, or
  `default.yml` across scopes) of an entry whose folder or local archive lies inside a project:
  `notes (<file>) installs from <path>, inside project <root>: changes there install without
  review wherever that file is on; lola keeps a project's local modules out of your files`, before
  anything is written. Moves between a project's own files are
  unaffected.
- A move between scopes whose source changed while you were asked: `extra changed in this project
  while lola was moving it; nothing was written`, hinting the move again.
- `on`, `off`, `switch` or `rm` without a scope found: `lola profile on needs a scope: …
  (default_scope: project or user)`, with one hint per scope (`lola profile rm x
  --project`, then `--user`); `default_scope` is a setting of `config.yaml`
  ([Choosing a scope](../guides/choosing-what-to-install.md#choosing-a-scope)).

A change line (`~ cc-skills-golang  897c0d803c71  …`) of `on`, `off` and `switch` names the
version the run installs: for an entry of yours, the one its pin in `pins.yaml` (user scope) or
`lock.json` keeps; an unpinned entry nothing has resolved yet still shows its source.

JSON:

- Listing and showing print `profiles` and `notices` (each profile's `shadowed_by` is the
  qualified name of the profile that hides it, or `null` when none does; showing adds each
  profile's `read_as`, for an active remote profile the user profile of its name read in its
  place, else `null`, and `modules`).
- `on`, `off` and `switch` print what `lola sync --json` prints plus `profile` (the profile
  on after the run, by qualified name, or `null`) and `changes`, and, with `--new`, `created` (one
  object per profile made, the members first: `profile`, `path`, `modules` (entries copied or 0),
  and `bundle` (the member names) or `from` (the profile copied), when they apply; `[]` when none;
  `--plain` prints a `created` row per profile, `would-create` under `-n`: profile, path).
- `split` prints `plan` (`bundle` and `profile`, each with `name`, `path`, `members`, `targets`
  and `modules`), `sync` (the sync document when the split profile is on, else `null`) and
  `dry_run` (`--plain` prints a `split` row for the bundle, with its members, and one for the new
  profile, with its modules).
- `add` prints `remote_profiles`, `notices` and `dry_run`.
- `update` adds `updates` and `scopes` (each `scope`, `root`, `apply` and `sync`).
- `rm` prints `profile`, `file`, `source`, `removed`, `trash` (where a profile of yours went,
  `""` for a remote one), `notices` and `dry_run` (`--plain` prints a `removed` row: the
  qualified name, the file, and the trash path, empty for a remote profile; `would-remove` under
  `-n`).
- `edit` with modules prints what `lola install --json` prints.
- `edit --bundle` prints `profile`, `path`, `members`, `was`, `created`, `sync`, `also` (the
  apply of each other scope the bundle reaches), `trust` (under `-n` of a bundle that is off, the
  trust question the run would ask; `null` otherwise), `notices` and `dry_run` (`--plain`: a
  `bundle` row, `would-bundle` under `-n`).
- `edit --new` without modules prints the sync document with `created`.
- `move` prints `context`, `edits` (a `removed` row for each entry that leaves a file and an
  `added` row for each that enters one, every removal first), `syncs` (what `lola sync --json`
  prints, one per scope applied, the target's first) and `created` (the path of the profile it
  made, absent when none).

Known limitations: lola checks the host of a remote profile and its entries by the URL's text and
never resolves it, so a host name that resolves to a loopback or private address (through
DNS or `/etc/hosts`) is fetched.

Two refusals hint advice, not a command: a remote profile entry
on this machine (`my-module: ./my-module is on this machine; a remote profile names only git
repositories and https archives`) hints `ask the profile's author to fix it; lola did not add
it`;

`rm` of a profile `go` that is a member of the bundle on hints `turn the bundle off first:
…`, then, for a bundle of yours, the run that takes it out (`or take go out of bundle mine: lola
profile edit mine --bundle …`), and for a project's bundle, which is the team's to edit,
`or remove go from the bundle: of <that bundle's file>` (with several files, each is named as it
spells the member).

An unknown name hints where to write a profile, a path ending in a placeholder, not a command:
`no profile absent for user scope` hints `write one: <config>/lola/profiles/<name>.yml; lola
profile add fetches a remote one` (at project scope it adds `, or the project's:
<project>/.lola/profiles/<name>.yml`).
