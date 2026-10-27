# Profiles

A **profile** is a named set of module entries, kept apart from any `project.yml` or `user.yml`.
lola reads your own profiles, in `$XDG_CONFIG_HOME/lola/profiles/<name>.yml`; a project's,
committed in `.lola/profiles/<name>.yml` (see [Project profiles](#project-profiles)); and remote
profiles, fetched from a source and kept in `$XDG_DATA_HOME/lola/profiles/<name>/` (see
[Remote profiles](#remote-profiles)).

**`default.yml`** is always on at user scope, and never at project scope: `lola sync --user`
installs what it lists as well as what `user.yml` lists. Turning it off means renaming or
deleting it. A profile with any other name is used only when it is **on** in a scope: each scope
has its own ordered active list, which `lola profile` changes and `lock.json` keeps (`profiles`,
each item's qualified name and the action that put it there), never committed:

```sh
lola profile go --on                 # append go to the active list, and apply
lola profile go docs --switch        # the active list becomes go, docs (default stays on)
lola profile go --off                # turn it off; asks before removing files (-y)
lola profile go --on --dry-run       # show what it would change
```

The actions have aliases for muscle memory from other tools: `--on` (`--enable`, `--activate`),
`--off` (`--disable`, `--deactivate`), `--switch` (`--use`), and for [remote
profiles](#remote-profiles) `--add` (`--get`), `--update` (`--upgrade`) and `--remove` (`--rm`,
`--delete`, `--del`, `--erase`, `--uninstall`); help shows them beside each flag, and messages use
the canonical names. An action names one or more profiles, in order; a name given twice refuses, and so do two
actions in one run (`--on and --off cannot be combined; pick one`). It resolves its scope as
`sync` does ([Choosing a scope](../usage.md#choosing-a-scope)) and applies only what the change moves: each
module is loaded, replaced (another layer's entry wins now) or unloaded (no layer wants it any
more; never one another layer still wants). Anything else the scope is waiting for, such as an
entry you added to `user.yml` and have not synced, is left to `lola sync`, with a notice (`2 other
changes are pending → lola sync --user`). Whenever the change removes files it asks first, on a
terminal; `-y` confirms, and off a terminal without `-y` it refuses, naming the command that
would, with the profiles by their qualified names. `--trust` and its
[kinds](../usage.md#trust-kinds) work as for `lola sync` (a project profile's changes are
`profiles`), except that the trust question offers only Trust all or Cancel: an action applies
all of its changes or none. The new list is written only with a successful apply: a No, a refusal, an error or
Ctrl+C leaves the old list and the old files. A profile already on (`user:go is already on (2nd
of 3)`), one not on, or a switch to the current list is a notice and changes nothing. `--on
default` is a notice; `--off` and `--switch` of `default` refuse (`rename or delete <path> to turn
it off`), and at project scope `default` refuses (`default applies to user scope only`). The
summary names each profile the run turns on or off with its file (`Profile user:go (<path>) → on, 2nd of 2`;
one it leaves as it was is its notice alone) and lists each change (`+ a  <version>  profile:user:go`, `~ c  …  profile:user:go overrides
profile:default`, `- a  …`) and is paged as `lola sync`'s diff is; `--json` gives the sync's
document, its `trust` object included, plus `profiles` (the active list after the run) and
`changes` (`{module, action: load|replace|unload, from, over}`).

An active profile whose file is gone is a warning (`profile user:go is on but no longer exists →
lola profile --user user:go --off`), and the run treats it as off; `--off` takes it by either
name. `lola uninstall` refuses a module an active profile lists (`<module> is wanted by profile
user:go`, hint `turn the profile off: lola profile --user user:go --off`), since the next sync
would install it again. For a module `default.yml` lists, which no command turns off, the error
says to remove it from `default.yml` and carries no hint.

A profile has the shape of a manifest, with an optional `description`:

```yaml
version: 1
description: Go tooling          # optional, at most 256 bytes
targets: [claude-code]           # optional; entries without targets use these
extends: [go]                    # optional: remote profiles whose entries come first
modules:
  - name: go-style
    source: https://github.com/example/go-style
    ref: v1.2.0
  - name: notes
    source: ../notes             # relative to this file's directory
```

- **Precedence.** At user scope: `default`, then the active profiles in list order, then
  `user.yml`. At project scope: the active profiles in order, then `project.yml`; `user.yml` never
  applies to a project. When two of them list one module, the later one's entry wins whole (no
  field is merged, [vars](templating.md#vars) included), with a notice when they differ: `gopls: user.yml
  overrides profile:user:go (v2.0 over v1.2)`, which adds `; gopls's vars from profile:user:go are
  dropped: the entry from user.yml sets none` (or `sets its own`) when the replaced entry set
  vars. `lock.json` records the profile that won (`from`).
- **Targets.** An entry without `targets` uses its profile's `targets`, else the scope manifest's.
- **Extends.** `extends:` names up to 16 [remote profiles](#remote-profiles), by their bare names
  (`go`, never `remote:go`), whose entries your profile, or a project's, builds on: their entries
  come first, in the order listed, then the profile's own, a later entry of a module replacing an
  earlier one whole; `lola profile <name>` shows the replaced remote profile as `(over
  profile:remote:go)`. An entry without `targets` takes the extending profile's. That is how a
  profile of yours named `go` builds on the remote `go` it shadows (`extends: [go]`). A name that
  is not a remote profile you have refuses with the file and line (`extends: go is not a remote
  profile you have`), whether it is missing, invalid or one of your own profiles; a remote profile
  cannot extend another, and one level is all there is. The extended remote profile's entries keep
  their pins, which only `lola profile remote:<name> --update` changes (`lola sync --update
  <module>` refuses them). In a project profile, the trust record notes the remote profile each
  entry comes from, with its source and pin, so after an update of it the entries it gives are
  asked about again.
- **Folders.** A relative folder or archive path resolves against the profile file's directory.
  Your profiles are trusted as `user.yml` is: their entries are never asked about, and their
  folders may be anywhere, even at project scope.
- **Pins.** lola never writes a profile. An unpinned git or archive entry installs what its ref
  serves when it is first synced, and `lock.json` keeps that version, which later syncs and
  `render` install, until `lola sync --update <module>` (with the scope's flag) resolves the ref
  again. To fix a version for good, write `commit` and `digest` into the profile yourself; a pinned profile entry
  installs that pin, and `lola sync --update <module>` refuses it (`edit the pin in <path>`),
  while `lola sync --update` without names leaves it at its pin, with a notice.
- **No manifest needed.** A scope with no `user.yml` (or `project.yml`) but a profile that lists
  modules syncs as if the manifest were empty, and so does one whose profile is gone but whose
  modules are still installed (they are removed); with none of these, `lola sync` still refuses.
- **Seeing them.** `lola profile` lists the profiles each scope can use, and `lola profile go`
  shows one: per scope whether it is on and, for each of its modules, the layer the scope takes it
  from (`b  user.yml  (over profile:user:go)`). A name is the profile's file name without `.yml`;
  `user:go` says it is yours, and `default` exists at user scope only. Files in `profiles/` whose
  names are not valid profile names are counted (`1 file with an invalid name in … is not a
  profile`), never shown. `--json` gives `profiles` (`name`, `kind`, `path`, `source`, `active`,
  `position`, `scope`, `shadowed_by`, `extends`, the remote profiles it extends, which only a
  profile's state fills in; with a name, also `modules` with `name`, `from` and `overrides`, and
  `read_as`, the profile of yours read in place of an active remote one, else `null`)
  and `notices`; `--plain` rows are scope, name, kind, `on`/`off`, position and
  path (with a name: scope, module, from and the overridden layers), followed by a `notice` row
  (`notice`, level, text) per notice, as `lola list --plain` has.
  At project scope a bare name is the project's profile or yours; a name both of them define is
  refused until you qualify it (`go is ambiguous: your profile user:go (<file>) and the project's
  project:go (<file>) are both named go`), with a hint for each that is your command, flags
  included, with the name qualified (`yours: lola profile --project user:go --on`). With `--off`,
  when only one of them is on the refusal says which (`go is ambiguous: your profile user:go
  (<file>) is on and the project's project:go (<file>) is off`) and hints only its `--off`; when
  neither is on, nothing changes and lola says so (`neither user:go nor project:go is on`), as for
  any profile that is off. `--off go` is refused too when both `go`s are on and both files are
  gone (a `--off` hint for each), and when the `go` that is on has lost its file while the other
  kind's exists and is off (`go is ambiguous: user:go is on but its file <file> is gone, and
  project:go (<file>) is off`), hinting the `--off` of the one that is on and, when the other is
  the project's, `lola profile --project project:go --on` to use it instead.
- **Reading.** A profile is read as strictly as `project.yml`: one YAML document of at most 1 MiB,
  version first (a newer lola's file asks you to upgrade lola), known keys only, at most 512
  entries, each checked as a manifest entry, and every refusal names the file and line. A profile
  may be a symbolic link (a dotfile manager's), as `user.yml` may; one that is not a regular file
  (a directory, a FIFO) is refused. `profiles/` may hold at most
  256 entries; a file whose name is not a valid module name followed by `.yml` is not a profile.

## Remote profiles

A remote profile is one someone shares from a git repository or an https archive. lola keeps
each in `$XDG_DATA_HOME/lola/profiles/<name>/`: `profile.yml`, the file as fetched, and
`source.json`, where it came from and its pin (the commit, and the digest of `profile.yml`). It
lives with lola's data, not the cache, so clearing the cache never drops a profile that is on. It
is used at user scope and in any project, by its name or as `remote:<name>`.

```sh
lola profile go --add https://github.com/example/profiles         # fetch profiles/go.yml, show it, ask
lola profile --add https://github.com/example/profiles            # on a terminal: pick among its profiles
lola profile go --add https://example.com/p.tar.gz --path team    # team/profiles/go.yml in an archive
lola profile go --add https://github.com/example/profiles -n      # show it, keep nothing
lola profile remote:go --on                                       # adding turns nothing on
lola profile go --update                                          # fetch it again, show the change, ask, apply
lola profile --update --all                                       # every remote profile, each asked on its own
lola profile go --remove                                          # delete it; asks (-y)
```

- **Adding.** `lola profile <name> --add <source>` (alias `--get`; `go` or `remote:go`) fetches the
  source, at `--ref` (a branch, tag or commit of a git source), into lola's staging and reads
  `profiles/<name>.yml` under its root, or under `--path`. The source must be a git repository
  (`https://`, `ssh://`, `user@host:path`) or an `https://` archive at a host named by name: a
  folder, a local archive, a `file://` URL, a URL lola cannot parse, plain `http://`, `localhost`
  and `*.localhost` (with or without trailing dots), a host given as a loopback, link-local,
  unspecified, private, carrier-grade NAT or site-local IP address (an IPv6 address that embeds an
  IPv4 one, such as `::127.0.0.1` or `64:ff9b::7f00:1`, counts as that address), and one whose last
  label is a number in any form but `a.b.c.d` (`2130706433`, `0x7f.1`, `127.1`) are refused (`…
  names the private address 10.0.0.7; a remote profile reaches hosts by name only, never inside your
  network; use the repository's https:// or ssh:// URL (or user@host:path), with its host name`),
  and so are a hostile ref, a remote helper (`ext::`), `git://` and credentials in the
  URL, before anything is fetched. Only `profiles/` is fetched, and a symbolic link in it, or on the
  way to it through `--path`, is refused before anything is read. The profile is read as strictly as
  any other, and each of its modules is fetched at its pin to show, under a header naming the file
  every scope shares and the scope it is compared with (`remote:go → <path> (shared by every scope);
  content compared with project <dir>`) and the profile's source and pin, every entry with all the
  MCP servers, hooks and files it brings, whatever that scope's `lock.json` says, which only notes
  when it has this version installed (paged, as `lola sync`'s diff is; printed without a pager, the
  files are capped with the `-n --json` command that lists them all, which names the profile even
  when you picked it: `lola profile remote:go --add <source> -n --json`); then it asks. Nothing is
  kept or cached before you say yes: a No or Ctrl+C leaves no trace. Other lola commands go on while
  it asks. Without a name, a terminal offers the source's profiles to pick from (filterable, nothing
  preselected; files whose names are not valid profile names, and `default.yml`, are only counted);
  off a terminal it refuses, naming them, with the command that adds each (that shows each, for
  `-n`). A `--path` the source does not have, and a source without `profiles/`, are refused saying
  which (`the source has no profiles/ directory at its root`). Off a terminal `--trust` (the
  `profiles` kind, all `--add` takes; any other kind, `--trust=entries,profiles` written out
  included, is refused) accepts what it shows, after its receipt, which on a terminal asks
  `Proceed?` (`--add` takes no `-y`); without it lola refuses off a terminal before any network
  contact, hinting the run with `--trust=profiles`; `-n` shows it and keeps nothing.
  A remote profile of that name already kept is refused (`lola profile remote:<name> --update`
  fetches it again; the hint keeps the run's flags but `--trust`, which answered the add, never an
  update's change), and so are the name `default`, a `user:` or `project:` name, and a file in the
  profile's place. `--add` asks no scope: the profile is shared by every scope, so `--project`,
  `--user` or `-C <dir>` only names the scope its content is compared with, and without them it is
  the project found from the working directory, else user scope (`--scope both` is refused); hints
  that repeat the run keep its flags (`--trust`, `-n`, `--json`, `--plain`, `--no-pager`; a hint
  that shows the profile, or adds it unasked, sets `-n` or `--trust` itself) and carry a scope flag
  only when you gave one. A `.lola/` in your home directory is never that project: user scope
  installs there, so it is skipped, with a notice (`<home> has .lola/, but it is your home
  directory, where user scope installs; it is not taken as a project`). When the found project
  cannot be read (its `lock.json` tracked by git, say), the refusal also offers the run at user
  scope (`or compare with user scope: lola profile --user go --add <source>`). Adding turns nothing
  on (`turn it on: lola profile --project remote:go --on`). `--json` gives `remote_profiles`
  (`profile`, `file`, `source`, `ref`, `path`, `commit`, `digest`, `compared_with`, `description`,
  `changes` as in the trust diff, `written`), `notices` and `dry_run`.
- **Updating.** `lola profile <name>... --update` (alias `--upgrade`; `go` or `remote:go`) takes one
  or more remote profiles' names, or `--all` for every remote profile you have, and fetches each
  again from the source, ref and `--path` its `source.json` records, which are checked as a typed
  source is. When the commit and digest are the same it says so (`remote:go is up to date with
  <source> (commit …, sha256:…)`) and changes nothing; otherwise it shows the old and new pins and
  how each entry changes (`~ example  rules (none) → false`, a new entry with what it brings, a
  dropped one with `-`, `(no entry changes)` when only the file's other fields changed), paged, and
  asks (`Update remote:go?`); `--trust` (its `profiles` kind) accepts it instead, and off a
  terminal without it lola refuses before any network contact. With `--trust` of any kind the
  update writes nothing before one receipt that lists every profile it covers and what each
  scope's sync covers: it keeps the profiles in memory, runs each scope's sync up to its receipt
  against them, then, on a yes, writes them and applies each scope; a No, or Ctrl+C at any
  question, updates nothing (`aborted; nothing was updated.`). A scope's sync refused before the
  receipt still keeps the update, after a receipt of the profiles, as below. Each profile is fetched, shown and asked about on its own, in the
  order named (by name for `--all`): a No keeps that profile and goes on to the next (`declined
  remote:go; it is kept as it was`); a profile that cannot be fetched, read, checked or written is
  kept as it was and reported (`✗ remote:go was not updated (the error says why)`), the run goes on,
  applies the others, and then fails, saying why each failed, with their hints (`1 of 2 remote
  profiles was not updated: remote:go: <why>`; a run of one profile fails with that profile's own
  error); Ctrl+C at a question stops the run: the profiles already updated are kept and not applied,
  and the message names the `lola sync` that applies them (`aborted; remote:docs is updated, but not
  applied (lola sync --user example); nothing else was updated.`), unless a profile failed before,
  or a scope could not be read once the profiles were written, which makes it an error (`… is
  updated, but not applied; the run stopped at Ctrl+C, and: …`, with that error's hints), and counts
  and names each profile the stop left with the failed ones (`2 of 3 remote profiles were not
  updated: remote:docs: <why>; remote:zed: stopped before it was updated; remote:go is updated, but
  not applied; the run stopped at Ctrl+C`). Ctrl+C while lola fetches or syncs ends the process, as
  for any lola command: what was written is kept, and the next `lola sync` of each scope applies it.
  When every profile is declined, nothing was updated (`aborted; nothing was updated.`, after the
  run's notices). A profile that changed while you were asked is kept as it was too (`remote:go
  changed while you were asked; nothing was updated`). Without names or `--all` it refuses, listing
  your remote profiles with both forms (`name them: lola profile remote:docs remote:go --update`,
  `or every one: lola profile --update --all`), which drop `--trust` and `-y`: those
  answered only for what you named; names with `--all`, and a name given twice, are refused, and
  `--all` is refused with every other action. The new profile replaces the old one in one step.
  `--update` asks no scope: the profile is shared by every scope, so lola then applies it in each
  scope it knows of that uses the profile (it is on, or an active profile or `default` extends it):
  the project found from the working directory, then user scope, as `lola sync --scope both` does.
  In each, it applies what the update moves there, the modules whose winning entry it changes, with
  a `lola sync` of them, asking that scope's own questions: before it removes files (`-y`), about
  hooks, and about any project trust the change needs (`--trust`). Without a scope flag, `--trust`
  answers only the remote profiles' consent, never the found project's trust (its `entries` and
  `profiles` kinds; the others, `hooks` say, still answer each scope's sync): on a terminal that
  question is asked; off one, a found project with changes you have not trusted refuses the update
  before anything is fetched (`project <dir> has changes you have not trusted (planted), which its
  sync asks about, and no flag named it: --trust then answers only for the remote profiles`),
  hinting the run with `--project`, or `--scope both`, and `--trust=` the kinds it needs. That check reads the project's
  trust before the update, and a project profile that extends the updated remote profile changes
  what that project trusts: an unscoped `--trust` run off a terminal can then write the profile and
  have the project's sync refuse. The error's `apply it later: lola sync --project …` hint applies
  it once you have reviewed the change (add `--trust`); naming the project up front (`--project …
  --trust`) avoids the stop. Any other change a scope was waiting for is left to `lola sync`, with a
  notice (`1 other change is pending → lola sync --user`); a scope where the update changes nothing
  installed, or that does not use the profile, says so (`remote:go is not in use in project <dir>`).
  Each scope's sync is reported after the update's own lines, under its own `project (<root>):` or
  `user:` line when both apply something. lola keeps no list of every checkout, so a note says the
  others catch up later (`every other checkout that uses remote:go picks the update up at its next
  lola sync`). A known scope lola cannot read refuses the update before the question, with the
  command that updates it in the other scope only (`or update it at user scope only: lola profile
  --user remote:go --update`), without `-y` or `--trust`, since the run's answers
  were given for every scope at once; without a scope flag, the message names the changes in the
  project you have not trusted. `--scope both` means the same
  as no scope flag (with `-C <dir>`, that project and user scope); with no `.lola/` project to name,
  or your home directory as that project (never taken as a found project either, with a notice), it
  is refused, hinting the run with `--user` in its place (`update at user scope only: lola profile
  go --update --user`; `-C`, `-y` and `--trust` are dropped too). `--project`, `--user` or `-C <dir>` alone narrows the
  update to that scope; `--project` or `-C` naming your home directory is refused before anything is
  fetched (`project scope at <home> is your home directory, where user scope installs`), hinting the
  run with `--user` in its place. The other scope you can see from there (user scope, or from user
  scope the project found from the working directory) is then read, never written: when it uses the
  profile a notice gives the command that applies it there (`remote:go is also on at user scope;
  apply it there: lola sync --user example`), and one that cannot be read is said so; a narrowed run
  that finds the profile unused says `every scope that uses it picks the update up at its next lola
  sync`. If a scope's sync stops, the update is kept, later scopes are not reached, and the error
  says so (`remote:go is updated, but not applied to the project: …; user scope was not synced`,
  with a hint `apply it later: lola sync --project example` for that scope and each one not
  reached). A conflict would stop that sync again: a file you edited by hand gives the hint `apply
  it later, overwriting what the conflicts list: lola sync --project example --force` instead
  (without `-y` or `--trust`), and a file another module owns gives none for that scope, the
  conflict naming what to remove; every such hint keeps `--json`, `--plain` and `--no-pager`; a No at that sync's own question is an abort, whose message names those commands (`…
  Apply it later: lola sync --project example, then lola sync --user example.`). Under `--json` the
  error document carries the run's document (`remote_profiles`, `updates`, `notices`, `scopes`
  done). Every hint that repeats the update keeps the run's flags (`--trust` as its kinds, `-n`,
  `--json`, `--plain`, `--no-pager`, `-y`), and a scope flag only when you gave one, except where a
  hint narrows the run to one of its scopes or names more profiles than you named (no `--trust` or
  `-y`). A `lola sync` hint keeps `--trust` (the kinds that answer a sync) and `-y` only when it
  names the modules the update moves; a whole scope's sync carries none,
  since it would answer for changes you never saw. A `--remove` hint never carries `-y`, except the
  `confirm it` hint of its refusal off a terminal, which asks for exactly that answer. `--update` of
  `user:go` or `project:go` is refused: those are files you edit. `--json` gives `remote_profiles`
  (with `previous_commit` and `previous_digest`), `updates`, one per profile named, in order
  (`profile`, `file`, `outcome`: `updated`, `up_to_date`, `would_update`, `declined`, `failed` or
  `skipped`, and for a failure its `error` and `hints`), `notices`, `dry_run` and `scopes`, one per
  scope the run covers, in order (`apply` empty and `sync` `null` where nothing moves; `[]` when
  nothing was written): `scope`, `root`, `apply` (the modules it moves there) and `sync`, that
  scope's sync document or `null`. A run that does not finish as asked (a failed profile, a scope
  not applied, a stop) prints that document as its error document, beside `error` and `hints`.
  `--plain` prints one row per profile: its outcome (`updated`, `up-to-date`, …), the profile and
  its file.
- **Removing.** `lola profile <name> --remove` (aliases `--rm`, `--delete`, `--del`, `--erase`,
  `--uninstall`) deletes a remote profile's directory, after asking on a terminal; `-y` confirms,
  and off a terminal without it lola refuses. It is refused while the scope you run it in, user
  scope, or the project found from the working directory uses the profile (`remote:go is on at
  user scope`, `remote:go is on in project <dir>`, or `remote:go is extended by user:mine, on at
  user scope`), hinting the `--off` that frees it, or the edit of the `extends:` that names it
  (the only way for `default.yml`), none of them carrying `-y`, which answers only this removal's
  question (`-n` and the run's `--json`, `--plain` and `--no-pager` are kept), and while one of
  them cannot be read to tell; lola keeps no list of every checkout, so other projects are not
  checked. `--force` removes it anyway, turning those checks into warnings, and answers the
  question, off a terminal too. Its modules stay installed until each scope's next `lola sync`,
  which warns that the profile is on but gone and removes them (asking first); a profile that
  extends it refuses until you edit its `extends:`. `-n` says what it would remove, and the command
  that removes it.
- **Your consent is its pin.** A remote profile is yours once you accept it, so its entries are
  never asked about in a project's trust diff. Every entry must carry its pin (`commit` and
  `digest` for git, `digest` for an archive) and name a git repository or an https archive over
  `https://` or `ssh://` at a host named by name, never a path on your machine, plain `http://`,
  `localhost` or an address inside your network (an IP address counts only in its `a.b.c.d`
  form: `2130706433` or `0x7f.1`, which git still connects to, is refused); one that does not, or whose `profile.yml` no longer has the
  digest `source.json` records, refuses every run that uses it, hinting the commands that replace
  it: `remove it: lola profile remote:go --remove --force` (it is in use wherever it was read,
  and no check of that can pass; `--force` answers its question, so the hint needs no `-y`), then
  `then add it again: lola profile go --add <its source>` when `--add` accepts the source
  `source.json` records, or why it does not. An entry's URL is never looked up
  on disk, so a directory a checkout names like it never stands in for it. `lola sync --update
  <module>` refuses a module whose pin a remote profile sets (`update it: lola profile
  remote:<name> --update`). lola does not resolve host names: a name that points inside your
  network, or a server that redirects there, is still fetched; add profiles only from sources you
  trust to that extent.
- **Local beats remote.** A profile of yours, or of the project at its scope, shadows a remote
  profile of the same name: a bare name means the local one, listings say `(shadowed by
  user:go)`, and `lola profile remote:go --on` is refused while it is shadowed (`use yours: lola
  profile user:go --on`, or build on it with `extends:`, or rename or delete yours). When you
  write a profile with the name of a remote one that is on, yours is used in its place from then
  on (`user:go now replaces remote:go`), while the active list keeps `remote:go`: delete yours and
  the remote one is used again, `--off` of it takes `remote:go`, and `--on` of yours changes
  nothing (`user:go is already on, in place of remote:go (1st of 1)`). A project profile never
  replaces it on its own: as for your profiles, lola says `project:go also exists → lola profile
  project:go --on`, and turning that on puts it in the remote profile's place as a trust change,
  dropping every other active profile of yours or remote one of its name.
- **Seeing them.** `lola profile` lists them with kind `remote`; `lola profile remote:go` shows the
  source, ref, commit and digest (`source`, `ref`, `commit` and `digest` in `--json`, `""` for a
  local profile) and its modules. An active remote profile that a profile of yours now shadows
  shows `user: 1st of 1, read as user:go; its modules:` and lists the remote profile's own modules.
  One that a project profile shadows stays in effect until you turn that on: `project (<dir>): 1st
  of 1, in effect; project:go would take its place if turned on`, with `to use project:go instead:
  lola profile --project project:go --on`. When both are on, it says so instead (`project:go is on
  too, 2nd of 2, and its entry wins where both list a module`): the later of the two in the active
  list wins. A name in `$XDG_DATA_HOME/lola/profiles/` that is not a valid profile name is counted, never shown; a name starting with `.` is a directory lola is
  writing.

## Project profiles

A project can commit profiles for its team in `.lola/profiles/<name>.yml`, in the same format.
They are listed and used at that project's scope only, by their file name or as `project:<name>`
(`lola profile project:go --on`), and turned on per checkout like any profile: committing one
turns nothing on. A project cannot define `default`: a `default.yml` there is not a profile, and
every listing says so (`a project cannot define default; rename <path>`).

A project profile came with the project, so lola treats it as it treats `project.yml`:

- **Reviewed before use.** The entries of every active project profile, and of one being turned
  on, are checked against what you last trusted, as `project.yml`'s are
  ([Sync, pins and trust](sync-and-trust.md#sync-pins-and-trust)): a pulled change is shown in the diff under its
  profile (`in profile project:go (.lola/profiles/go.yml):`) and needs your answer or `--trust`
  before anything is fetched. A change to a profile that is off is reviewed when you turn it on.
  Only the entries that win are checked: an entry another layer overrides (`project.yml`'s, or a
  later profile's) is reviewed, with what it installs, when it starts to win, whatever makes it
  win (a sync after `project.yml` drops the module, or turning the other profile off), so one
  profile may be asked about in two steps.
  An entry without `targets` installs to the profile's `targets`, so a change to those is a
  change to each such entry. `render` leaves out a module whose project-profile entry you have
  not trusted, as it does for `project.yml`'s.
- **Pinned in place.** `lola sync` pins an unpinned git or archive entry, with your trust, by
  writing `commit` and `digest` into the profile file (comments and other keys kept), and
  `lola sync --update <module>` re-pins it, as for `project.yml`. Commit the result so your team
  installs the same version.
- **Never replacing yours silently.** When a project gains a profile with the name of one of
  yours that is on there (`user:go`), lola leaves yours on: `lola sync` (with or without
  `--trust`), other `lola profile` actions and listings say `project:go also exists → lola profile
  project:go --on` (with the scope's flag). Turning it on, by `--on` or by naming it in
  `--switch`, puts it in your profile's place in the active list, keeping its position: the
  action's diff shows `~ project:go now replaces user:go (a, b)` with the modules it changes,
  and its all-or-none trust question (or `--trust`) covers it. Cancel, and yours stays on.
- **Kept inside the project.** Relative folder and archive sources resolve against
  `.lola/profiles/`, and like `project.yml`'s must resolve inside the project, links followed.
  The diff and `lock.json` show them relative to the project root, as `project.yml`'s are
  (`../../mods/a` in the profile shows as `mods/a`). The profile files, `.lola/` and
  `.lola/profiles/` must be the project's own: a symbolic link there is refused before anything
  is read, by every run at project scope with any profile on, yours included.
