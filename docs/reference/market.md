# `lola market`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola market [<name> [<entry>] | --check | [<name>] --add <source> [--ref <ref>] [--path <file-or-dir>] | <name>... --update | --update --all | <name> --remove | --known [<id>] | [<name>] --enable <id>]
```

Aliases: `marketplace`, `markets`. On a terminal, bare `lola market` opens [the picker](#the-picker).
Six read-only paths, none of which asks:

- `lola market`, off a terminal or with an off switch (`--json`, `--plain`, `--no-pager`,
  `--no-interactive`, `market_picker: false`), lists your
  [marketplaces](../guides/marketplaces.md#marketplaces): format, entries, skipped entries, when each
  was fetched and its source.
- `lola market --check` lists them with each git source's upstream status ([`--check`](#--check)).
- `lola market acme` shows one: its source, pin, every entry with the source lola maps it to, and
  every entry it skips, with why. Its last line is the next step, `→ lola info @acme/<first
  entry>`, with the run's flags.
- `lola market acme formatter` shows the entry: it is the same run as `lola info @acme/formatter`,
  with the same flags (a scope, `-t`, the output flags) and the same output, hints included.
- `lola info @acme/formatter` (see [`lola info`](info.md#lola-info)).
- `lola market --known` lists the [known marketplaces](../guides/marketplaces.md#known-marketplaces)
  this lola ships; `lola market --known superpowers` shows one.

`--add <source>` takes the source, so the name goes before it: `lola market acme --add <source>`,
not `lola market --add acme <source>` (refused, hinting the working order; see Refusals below).

It fetches a catalog (Claude Code's `marketplace.json` or a lola YAML catalog) and
registers it. lola looks for `.claude-plugin/marketplace.json`, then `<name>.yml`, `<name>.yaml`,
`marketplace.yml` and `marketplace.yaml`, and last, only when none of those exists, the original
lola's `lola.yml` or `lola.yaml` (`name` and `modules`, each with `name`, `description`,
`version`, `license`, `repository`, `ref`, `path` and `tags`; `ref` is the branch, tag or commit of a git
`repository`, and an entry that gives one for an archive is skipped), at the root or in the
`--path` directory; `--update` fetches the named ones, or `--all`, again and reports which entries
changed; `--remove` forgets one (asks; `-y`).

Cursor's and Copilot's catalogs are not searched for; read one with `--path .cursor-plugin/marketplace.json` or `--path .github/plugin/marketplace.json`, where a source written without `./` (`teaching`, `plugins/x`) is a directory under the repository root.

Cursor's `.cursor-plugin/marketplace.json` and GitHub Copilot's `.github/plugin/marketplace.json`
are the alternate catalogs: lola never reads one unasked, but names it. A source with no catalog
at its root and an alternate is refused with one `lola market <name> --add <source> --path <file>`
hint per alternate it holds; likewise a `--path` directory with no catalog that holds a bare
`marketplace.json` (`--path .cursor-plugin`) is refused with the `--path` of that file. A source with none and a module's layout (`SKILL.md`, `skills/<name>/SKILL.md`, `mcps.json`,
or `.md` files in `commands/`, `agents/` or `rules/`: what `lola install` reads) is refused as "a module, not a catalog", with
`lola install <source>` as the hint; any other tree is refused with the places lola looked and no
hint. When lola reads a catalog at the default location and an alternate is also there (the two can
differ: `cursor/plugins` lists 2 entries in one and 155 in the other), `--add` adds the notice
`the source also has <file>, which lola did not read`, in the human output and the JSON `notices`
array, with the `--add` of the alternate under a free name. `--update` reads the file the
registration recorded and says nothing of alternates.

`--add` and `--update` ask nothing, but a name when
yours is taken and, on a terminal, which of the ssh hosts a catalog names to allow (below).

Scope flags (`--scope`, `--user`, `--project`, `-C`, as for [`lola info`](info.md#lola-info))
change nothing but what `lola market <name> <entry>` plans for.

`add`, `ls`, `list`, `rm`, `remove`, `update`, `set`, `show` and `search` are not `lola market`
verbs: each is refused with the lola command to run instead.

| Flag                                         | What it does                                                                                                                                                      |
|----------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--add <source>`                             | Fetch the catalog at this source and register it. Alias: `--get`.                                                                                                 |
| `--ref <ref>`                                | With `--add`: the branch, tag or commit of a git source.                                                                                                          |
| `--path <file-or-dir>`                       | With `--add`: the catalog file, or the directory to look for it in, inside the source.                                                                            |
| `--update`                                   | Fetch the named marketplaces' catalogs again and report what changed. Aliases: `--upgrade`, `--refresh`.                                                          |
| `--all`                                      | With `--update`: every marketplace you have.                                                                                                                      |
| `--remove`                                   | Forget the named marketplace and drop its cached catalog (asks; `-y`). Aliases: `--rm`, `--delete`, `--del`, `--erase`.                                           |
| `--check`                                    | List your marketplaces with each git source's upstream status. Asks each remote for its tip; fetches no catalog. |
| `--no-interactive`                           | List as text, never the picker. `market_picker: false` in `config.yaml` turns the picker off for every run. Refused with a name, an entry or an action, which never open the picker. |
| `--known [<id>]`                             | List the marketplaces this lola ships, each pinned to a commit, or show the one whose id you give. Enables nothing. |
| `--enable <id>`                              | Register the known marketplace `<id>` at its pinned commit, under the name `<name>` (default: the id). Shows what it holds and asks; `-y` skips the question. |
| `--ssh-host <host>`                          | With `--add` or `--update` (one marketplace): let the catalog's ssh sources at this host name use your ssh keys. Repeatable. With `--remove`: stop allowing it.   |
| `-y`, `--yes`                                | With `--remove` or `--enable`: do it without asking.                                                                                                                        |
| `-n`, `--dry-run`                            | Report what the action would do, and write nothing.                                                                                                               |
| `-t`, `--target <assistant>`                 | With `<name> <entry>`: the assistants to plan for, as `lola info`'s `-t`.                                                                                         |
| `--scope`, `--user`, `--project`, `-C <dir>` | With `<name> <entry>`: the scope to plan for, as `lola info`'s. With any other action they change nothing, but one naming a scope that cannot be used is refused. |

A catalog's `ssh://` and `user@host:path` sources are skipped unless you allowed their host. The
host is a name alone (no scheme, user, port or path; lower-cased; never an IP address or
`localhost`), and the allowance is per host, whatever the user or port.

An entry whose text holds a line break or a control or invisible character (a description with a
newline, say) is skipped, with that reason, under its name or under `plugins[N]` (`modules[N]` in
a YAML catalog) when the name is the offending text. The same text in the catalog's own `name`,
`description`, `version`, `owner` or `metadata` refuses the catalog. lola never rewrites a
catalog's text.

On a terminal, `--add`
and `--update` list the hosts the catalog names that you have not allowed, with their entry
counts, all unticked. Off a terminal, with `-n` or under `--json`, nothing is asked: a notice
gives the command with `--ssh-host`.

`--update` never widens the list on its own, and a host the
catalog stops naming stays allowed until `--ssh-host <host> --remove`, which re-applies the
cached catalog; a plain `--remove` forgets the hosts with the marketplace. `lola market <name>`
shows them (`ssh hosts`, `via ssh at <host>`). Every other source must still be `https://`.

Refusals:

- A catalog file at an https URL whose certificate no trusted authority signed, hinting `trust your
  certificate authority: set SSL_CERT_FILE=<path to its PEM>` (the only hint a download failure
  carries here).
- An unknown name: `you have no marketplace nope`, hinting `lola market`.
- Three or more names without an action: `lola market <name> <entry> shows one entry, and 2
  were given: formatter, linter`, hinting `lola market acme formatter` and `lola market acme
  linter` for the given entries the catalog lists and does not skip; when none is, `lola market
  acme`, or `lola market` when `acme` is not yours.
- `-t` without `<name> <entry>`: `--target applies to lola market <name> <entry> only`, hinting
  the run without it.
- `--update` without names: `name the marketplaces to update, or update every one with --all`,
  hinting `lola market --update --all`.
- A reserved word: `ls is not a lola market verb: lola market lists your marketplaces`, hinting
  `lola market`; `add` hints the `--add` it means (`lola market acme --add
  https://github.com/acme/plugins`).
- `--check` with a name, an entry or an action (`--add`, `--update`, `--remove`, `--known`,
  `--enable`, a reserved word): `--check lists your marketplaces' upstream status; it takes no
  name, entry or action`, hinting the run without `--check`, then `lola market --check`.
- A source where the name goes (`lola market --add acme https://github.com/acme/plugins`): the
  source is refused as a marketplace name, hinting the run with the two swapped (`lola market acme
  --add https://github.com/acme/plugins`).

JSON:

- Listing prints `markets` (one row per marketplace) and `notices`. With `--check`, each row adds
  `status` (`up to date`, `update available`, `upstream moved` or `unreachable`; `""` for a source
  that is a local path, a `file://` URL, an archive or a catalog file, or a catalog never fetched); without it the rows have no `status`.
- Showing prints the marketplace's `name`, `source`, `ref`, `path`, `format`, `commit` (the commit
  fetched), `fetched_at`, `description`, `file` (the catalog file read), `owner`, `version`,
  `ssh_hosts`, `entries` (each with its `ssh_host`), `skipped`, `notices` and `hints` (`lola info`
  of the first entry; `[]` without entries).
- `<name> <entry>` prints what `lola info` prints.
- `--add` and `--update` print:
  - `markets`, the marketplaces fetched;
  - `changes`, how the fetch changed each entry (`market`, `entry`, `change`: `added`, `removed`
    or `changed`, and `fields`, which of `source`, `path`, `ref`, `commit`, `version` and
    `components` differ);
  - `updates`, one per marketplace of the run, in the order named (`market`, `outcome`, and for a
    failure its `error` and `hints`);
  - `notices` and `dry_run`.
- `--remove` prints `removed` (the names forgotten), `notices` and `dry_run`.

Known limitation: a catalog entry's host is judged by the URL's text, as a [remote profile's](../guides/remote-profiles.md) is,
and never resolved: an entry at a host name that resolves to a loopback or private address is
mapped and fetched.

## `--check`

`lola market --check` lists your marketplaces as `lola market` does, with each git source's
upstream status: a `STATUS` column in the table (`-` for a source with none), the last column in
`--plain`, `status` in `--json`. It asks each remote for the tip of the registered ref (the default
branch when the ref is a commit), 8 at a time, 10 seconds each, and fetches no catalog. A source
that is a local path, a `file://` URL, an archive, a folder or a catalog file has no status, and
neither has a catalog never fetched. It is the path for scripts; the picker shows the same
statuses.

| Status             | Meaning                                                                                                                                  |
|--------------------|------------------------------------------------------------------------------------------------------------------------------------------|
| `up to date`       | the remote's tip is the commit of your cached catalog                                                                                    |
| `update available` | the tip is another commit on the branch, tag or default branch the marketplace follows: `lola market <name> --update` fetches it         |
| `upstream moved`   | a marketplace registered at a commit (a known one on lola's pin or ahead of it, or yours added with `--ref <commit>`) whose remote has moved on: `--update` cannot move it; `u` in [the picker](#the-picker) moves it to the tip after a confirmation |
| `unreachable`      | the check failed: no network, a host that is down, a repository gone or private, or a ref the remote does not have                      |

Without `--check`, `lola market` contacts no host.

## The picker

On a terminal (stdin and stderr), bare `lola market` opens a picker over your marketplaces, then
the known marketplaces you have not enabled (an enabled one is among yours, once), and
`Add a source…` last. It does not open with `--json`, `--plain`, `--no-pager` or
`--no-interactive`, or with `market_picker: false` in your `config.yaml`
([config](../guides/config-and-selections.md)); the run lists instead. A stdout piped from a
terminal still gets the picker: scripts should pass `--plain` or `--json`.

Each row is up to three lines: the name (a known one's id), its ref (a known one you have not
enabled: `pin <commit>`), its entries, `known`, `ahead of pin` (an enabled known marketplace moved
past lola's pin) and its status; then its description, dim, indented under the name and wrapped to
the terminal (the catalog's, or a known one's reason when the catalog has none; left out when
there is neither); then its source on a line of its own, indented the same.

Opening the picker, and moving, ticking and filtering in it, contact no host and show no status.
`u` checks the ticked rows, or the cursor's when none is ticked, as `--check` does; `U` checks every
row, ticked or not, whatever the filter shows. Each row checked reads `checking…` until its answer
comes: `up to date`, `update available`, `upstream moved` or `unreachable`. A known marketplace you
have not enabled is checked against lola's pin. A local, archive, folder or catalog-file source
gets no status.

Once every answer is in, the footer asks once, in place of the keys: `update acme, beta? ⏎/y yes ·
esc/n no` (`update 3 marketplaces? …` when the names do not fit). It names the rows of yours that
read `update available` or `upstream moved`, and those of yours it cannot check (local, archive,
folder and catalog-file sources). Enter or `y` updates them, as `--update` does, and moves each
`upstream moved` one to its remote's tip after `Update past the commit it is registered at?` (No leaves it as it was); Esc or `n` keeps
the picker open with the statuses shown; `u` or `U` drops the question and checks again, as they do
with none open. A known marketplace you have not enabled is never offered:
Enter enables it. With nothing to offer, a note says why (`everything checked is up to date`,
`nothing to update; 1 unreachable`, `nothing to update; ⏎ enables a known marketplace`).

When at least two checks failed and more than half of those checked did, one banner says `Several
sources unreachable; check your network connection`, the failed rows read `not checked`, and
nothing is offered. Otherwise each failed row reads `unreachable`, with the error under it and what
to do: `→ u retries · lola market <name> --remove forgets it` for one of yours, `→ u checks again`
for a known one. When the only check failed, the line starts `→ check your network connection;`.
Nothing is removed for you.

| Key       | What it does                                                                                                    |
|-----------|-----------------------------------------------------------------------------------------------------------------|
| `x`       | Tick or untick the row (`space` too).                                                                           |
| `ctrl+a`  | Tick every row shown, or none. Not listed in the footer.                                                       |
| `/`       | Filter by name, source, reason or description; `a`, `o`, `u`, `U`, `q` and `x` are text there. Enter keeps the filter, Esc drops it. |
| `u`       | Check the ticked rows, or the cursor's, then offer to update those of yours that need it.                       |
| `U`       | Check every row, then offer to update those of yours that need it.                                              |
| `o`       | Open the cursor row's web address in your browser (see [below](#entries-and-an-entry)); a row with none says `no web address for <id>`, and `Add a source…` says `nothing to open on Add a source…`. A local, archive or catalog-file source has no web address. |
| `a`       | Add a source: asks its source and an optional ref, then runs `--add` with its questions. The new marketplace is ticked when the picker opens again. Enter on `Add a source…` does the same. |
| Enter     | With nothing ticked and the cursor on one of yours: open its [entries](#entries-and-an-entry). On a known row you have not enabled, [preview it](#previewing-a-known-marketplace): fetch its catalog at the pin and open its entries; nothing is added. With rows ticked: enable each known one, then `Do what with these?` for yours. |
| `m`       | The menu for the ticked rows, or the cursor's (on a known row, this is how to enable it without previewing it): enable each known one you have not enabled, asking as `--enable` does; then `Do what with these?` for yours: Browse entries (`lola install @<name>`'s picker), Update, Remove (asking as `--remove` does), Cancel. |
| Esc, `q`  | Leave (Esc first clears a filter; `q` at the update question leaves too). Changes nothing; exit 0.              |
| `→`       | The same as Enter on one of your marketplaces with nothing ticked: open its entries; on a known row you have not enabled, preview it as Enter does. It does nothing on `Add a source…` or with rows ticked. `←` does nothing here; it never leaves. |
| Ctrl+C    | End the run: `✗ cancelled — nothing was changed`, exit 4, at every step but two. In a `[y/N]` question an action asks, it answers No and the picker opens again. At the ssh-host question that moving an `upstream moved` marketplace to its remote's tip can ask after `Update past the commit it is registered at?`, it writes nothing for that marketplace and the picker opens again, as No does. |

Taking the update for a marketplace registered at a commit (a known one on lola's pin or ahead of
it, or one of yours) asks `Update past the commit it is registered at?` over the catalog changes it
brings, then rewrites the registration's `ref` to the remote tip's full commit SHA and fetches it;
No writes nothing. A known one then reads `ahead of pin`, and later `--update` runs stay at that
commit.

After each action the picker opens again, read afresh (ticks and statuses dropped); an action that
fails prints its error and hints first. Leaving then exits 0.

### Entries and an entry

The list, the entries and the detail pages all print the context line under their title: the scope
the picker acts on (see below). Enter on one of your marketplaces, with nothing ticked, opens its
entries: the marketplace's name, the context line, then each entry as a checkbox (`[ ]`, or `[x]` once ticked), its name and version,
`installed` or `cached`, and, under it and two columns right of its name, its description. The page is read from your cache and contacts no host;
a marketplace whose catalog was never fetched, or that lists no entries, says so (`u` fetches it) and
the list stays.

| Key        | What it does                                                                                         |
|------------|------------------------------------------------------------------------------------------------------|
| `/`        | Filter by name, description or tag. Enter keeps the filter, Esc drops it; `i`, `d`, `o` and `x` are text there. |
| `x`, space | Tick or untick the cursor entry. The footer then names `N ticked`, and `N ticked (M hidden)` when the filter hides M of them. Ticks last while the marketplace is open, through a filter and an entry's detail page, and are dropped when another marketplace opens or an action ends. |
| Ctrl+A     | Tick every entry the filter shows, or untick them when all of them are ticked; entries the filter hides keep their ticks. |
| Enter, `→` | Open the cursor entry's detail page.                                                                 |
| `i`        | Install the ticked entries the filter shows, or the cursor entry when none is ticked, through the regular install (below), in one `lola install` call. The footer names it only when the cursor entry, or with ticks one of the ticked entries, is not installed (`i install`). |
| `d`        | Uninstall the shown ticked entries that are installed, in one `lola uninstall` call, or the cursor entry when none is ticked. The footer names it, with the scope it acts on, only when the cursor entry, or with ticks one of the ticked, is installed (`d uninstall from user`); with ticks and none of them installed it says `none of the ticked entries is installed` and runs nothing. |
| `o`        | Open the cursor entry's web page in your browser (see below). The footer names it only for an entry that has one. |
| `←`        | Back to the list, as Esc does, but at once even with a filter set (Esc drops the filter first).     |
| Esc        | Back to the list (first drops a filter).                                                             |
| `s`        | Open the [skipped entries](#skipped-entries), when the catalog skipped any. The footer names it (`s skipped`) and says `N entries skipped · s shows why` only then. |
| `q`        | Leave the picker, from here or from the detail page.                                                 |

`i` and `d` never act on a ticked entry the filter hides: with ticks but none shown they say `ticked entries are hidden by the filter; esc clears it`, run nothing and keep the ticks.

A ticked entry that is already installed is passed to `i` along with the others: `lola install` of a
module that is installed and unchanged reports `already up to date` for it and goes on, so the call
still installs the rest. When every ticked entry is installed `i` says `already installed; d
uninstalls` and runs nothing. After the install or uninstall the picker opens again on the entries
page, the cursor on the first entry acted on, and the ticks are dropped. A preview (a known marketplace
you have not enabled) has no checkboxes and ignores `x` and Ctrl+A, since nothing in it can be installed.

#### Skipped entries

`s` on the entries page, of one of yours or of a preview, opens a read-only page titled `<id> — N
skipped entries`: each entry the catalog lists that lola does not offer, by name, with the reason
under it, indented two spaces and wrapped (the same names and reasons `lola market <name>` prints
under `Skipped`). Up, Down, `j`, `k`, `g`, `G`, `pgup` and `pgdown` scroll it; Esc or `←` go back to the
entries with the cursor and scroll as they were, and `q` and Ctrl+C leave as elsewhere. Your own
marketplaces' skipped entries are read from the cached catalog, so the page contacts no host. In
accessible mode `s` at the entries prompt prints the same page, then asks again.

The detail page shows the entry's name and version, then under it, indented two spaces, its
description, tags, source URL (without user info, query or fragment) and `license:` (as the catalog entry states it; no line when it states none, or one that is not text, is over 256 bytes or holds a line break or control characters, which lola drops without skipping the entry), whether it is installed and at which commit or digest it was recorded,
and `in your cache` when its source is cached. One blank line separates the description, the tags,
URL and license, and the installed line, whichever of them the entry has. On it `i`, `d` and `o` act as on the entries page, on
this entry. `i` installs the entry and `d` uninstalls it (only the one that applies is in the
footer); on the other, the note says why (`already installed; d uninstalls`, `not installed; i
installs`). They run `lola install @<marketplace>/<entry>` or `lola uninstall <entry>` with their
usual questions and output, then the picker opens again on the same entry with its installed state
read again: on its detail page when you pressed the key there, on the entries page with the cursor on
it when you pressed it there. `←` and Esc go back to the entries.

`o` opens the entry's source in a browser so you can read the material before you install it; on
the list it opens the marketplace's repository the same way, from its registered source and ref. The
address is derived from the catalog's source for the entry: an https (or http) git remote as it is,
`.git` dropped and any credentials, query or fragment left out; an ssh remote of `github.com`,
`gitlab.com`, `codeberg.org` or `bitbucket.org` at the same repository over https; and, on GitHub
and GitLab, the entry's directory at its ref when it has a path. An entry that is a folder, an
archive or an ssh remote of another host has no web page, and `o` says so. Only `http://` and
`https://` addresses are ever opened: one with a space, a control or non-ASCII character, another
scheme, credentials, no host or a host with anything but letters, digits, dots and hyphens is refused,
and nothing is launched. The browser is started directly (`xdg-open`
on Linux, `open` on macOS, `rundll32 url.dll,FileProtocolHandler` on Windows) with the address as its
one argument, never through a shell, in a session of its own on Linux and macOS so a text browser
cannot take the picker's terminal. The note says `opened <address>`; when the launcher is missing or
fails it says what failed and gives the whole address to copy, and the picker stays open.

Installed is read, and `d` acts, in one scope: the one the context line names, `user` or the
project's. The picker never asks for it and ignores `default_scope`: it is the nearest project with a
`.lola/` directory above where you run it, else user scope. `--scope`, `--user`, `--project` and
`-C` pick it instead; `--scope both` is refused, since the picker reads one scope. `d` runs
`lola uninstall` pinned to that scope and names its kind, `user` or `project`, on its key on the
entries and detail pages and in the accessible prompts. `i` runs the regular install with the scope
flags you gave `lola market` and no others, so the install resolves its scope itself as
[`lola install`](install.md) does: the flag, else the terminal's scope picker, else the project with
`.lola/`, else `default_scope`, and its error with hints when none resolves. `i` names no scope and
does not need the picker to have one. A preview names neither key: `i` and `d` do nothing there. When you install
into a scope other than the one the picker reads, the entry still shows `not installed` when the picker
opens again. An entry shows installed when the scope read holds a module of its
name, from whichever marketplace. When that scope's `lock.json` cannot be read, the picker prints the
error with its hints as a warning and opens without entries, so adding, updating and removing
marketplaces still work.

In accessible mode (`LOLA_ACCESSIBLE=1` or `TERM=dumb`) the picker is a numbered list with no
status and no `u` or `U`, under its title and the context line. Each page is numbered and printed
once, then asks a line at a time; Enter alone goes back a level, and from the list leaves. There is
no filter.

| Page    | Prompt                                                                                         | Answers |
|---------|------------------------------------------------------------------------------------------------|---------|
| list    | `Choose marketplaces (numbers, comma separated; m1 for the menu; o1 for the web page): `               | One number of yours opens its entries; one known row's number previews it (below). Several numbers, or `m1`, `m1,2` (also on a known row), answer the menu for those rows. `o1` opens row 1's web address as the TUI's `o` does, with the same notes, and the prompt comes again; `o` takes one number. `Add a source…`'s number alone adds one. Each row prints its description and source under its number, as the TUI does. |
| entries | `Choose an entry (number or key above): ` under the line `i<n,n…> install · d<n,n…> uninstall from project · o<n> web · Enter alone goes back` | `n` opens entry n's detail; `i<n>` installs it, `d<n>` uninstalls it, `o<n>` opens its web page. `i` and `d` take several numbers, comma separated (`i1,3,5`, `d2,4`), and act on them as the TUI's do on ticked entries, in page order, in one call; `o` takes one. `i`, `d` and `o` apply as the TUI's keys do, so an entry that does not take one prints the same note (`already installed; d uninstalls`, `not installed; i installs`, `no web page for this entry`) and the prompt comes again; so does a letter with no number, a number out of range or a number named twice in a list (`name each number once`), or any other text. |
| detail  | `i install · o web · Enter alone goes back: ` | The letters it names: `i` or `d` (only the one that applies is named, the other prints its note), `o` when the entry has a web page. |

Accessible output is laid out for the terminal's width less one column, so the cursor never sits
at the wrap edge (with an unknown width nothing wraps). Lines wrap at spaces only: a word is never
split, not at a hyphen either, so an address or a flag in a note stays whole even when it is wider
than the line. The title, the context line, entry names and descriptions and the notes wrap this way
under their numbers or indent. The entries page prints its keys as lines of that width, each key
whole, above the short prompt; the list and detail prompts do the same (`Choose marketplaces: `, its keys then `numbers, comma separated · m1 for the menu · o1 web`;
`Choose a key above: `) only when the full prompt is not narrower than the terminal. The
`fetching…` line drops ` from <host>` for a source with no host (a `file://` one).

The scope kind on the uninstall prompt is the context line's (`from user`, `from project`). `o` uses the same
opener and the same address check as the TUI and prints `opened <address>`, or what failed with the
whole address to copy; the prompt comes again either way.

### Previewing a known marketplace

Enter or `→` on a known row you have not enabled, with nothing ticked, shows what it offers before
you enable it. This is the one thing the picker does on its own that contacts a host: it fetches the
catalog at the commit lola pins for it (what `lola market --enable <id> -n` reads), and while it runs
the footer says what is going on, `fetching <id> at <commit> from <host> — nothing is added or
installed…`. The picker stays usable meanwhile (in accessible mode the line prints before the fetch starts and the prompt waits for it). Esc cancels the fetch, `q` and Ctrl+C leave and
cancel it, and so does opening one of your marketplaces or starting another known row's fetch.

The fetch adds nothing: it writes no registration to `markets.yaml` and nothing to your marketplace
cache, and the tree it fetches lives in a staging directory that is removed when it ends. Nothing
in it runs. Leaving with `q` or Ctrl+C mid-fetch can leave that directory until the next lola run clears it. Enabling afterwards fetches the pin again.

The entries then open as for one of yours, under the title `previewing <id> — not added; pinned to
<commit>, curated <date>` (the date the maintainers reviewed that pin) and the context line. Filter,
Enter and `→`, `o`, `←` and Esc work as above, and `installed` is read for the context line's scope.
When the catalog lists entries lola does not offer (a source it does not take, an ssh host you have
not allowed, a directory the tree lacks), a line says how many, `3 entries skipped · s shows why`, and `s`
opens the [page that lists them](#skipped-entries); those entries are not in the list.
What differs: `i` and `d` only say `not added yet; m enables <id>`, and `m` (named in the footer in
place of `i` and `d`, and so on the accessible prompts) leaves the picker as `m` on the known row does, asking as `--enable` does. Once
enabled, the marketplace is one of yours, with its catalog cached. A preview you fetched stays for
the session, so going back and in again fetches nothing; it is of a pinned commit, so it cannot go
stale.

When the fetch fails (offline, a pin the repository no longer has, a catalog lola cannot read) the
list stays, with the note `could not fetch <id>:` and the reason (`<id>: could not fetch <url>: …` when the reason already says so), wrapped so all of it shows, and a
command that retries it (`lola market --enable <id> -n`); no empty entries page opens. A catalog with no entries says so the
same way. A result that arrives while you type a filter or answer an update offer does not move the
screen: it is kept, the note says `<id> fetched; ⏎ opens it`, and Enter opens it. Opening a row of
yours without entries while a fetch runs cancels it and says so.

Accessible mode previews the same way: a known row's number, with nothing else chosen, prints the
`fetching…` line, then fetches; Ctrl+C cancels the fetch and aborts the run (exit 4, `nothing
changed`). A failure prints the `could not fetch <id>:` note with its hint, and the market prompt
asks again. On success it prints the `previewing <id> — not added; pinned to <commit>, curated
<date>` title, the context line, the entries, the skipped-entries line and any warning, then the
entries prompt, `Choose an entry (number or key above): ` under `m enable · s skipped · o<n> web · Enter alone goes back` (`s skipped` only when the catalog skipped entries). There `i<n>`
and `d<n>` (`i` and `d` on a detail page) print `not added yet; m enables <id>`, `o<n>` opens the page,
and `m` answers as `m` on the known row does. A fetched preview is kept for the session; going back
and choosing the number again fetches nothing.

## Known marketplaces

`--known` lists what this lola ships: for each, its `id`, whether you have enabled it, the day the
maintainers reviewed it, the first seven digits of its pin, its source and why it is listed.
`--known <id>` shows one in full (source, path, the whole pin) and hints the next step: the
`--enable` that registers it, or `lola market <id>` once enabled. A marketplace counts as enabled
when one named as the id is registered from that source. An id lola does not ship is refused,
naming the ids it does, hinting `lola market --known`. With none shipped, `--known` prints
`no known marketplaces ship with this lola` and exits 0.

`--enable <id>` registers the entry at the commit it is pinned to, never a branch's head, through
the same fetch as `--add`: the catalog is read as any other and nothing in it runs. The pin covers the catalog and the entries it keeps in the same repository; an entry whose source is another repository is fetched at the ref the pinned catalog names for it, which may be a branch. The name is the
id, or the one `<name>` gives (`lola market mine --enable superpowers`). It fetches the pinned catalog,
shows the entry's facts and what the catalog holds (entries, and how many it skips), and asks;
`-y` answers. Off a terminal without `-y` it refuses, `lola market --enable superpowers -y` as the hint,
having written nothing; under `--json` the refusal carries the plan, the keys `-n --json` prints.
`-n` shows the same and writes nothing, cache included. It never asks for another name or an ssh
host: a name another marketplace holds is refused with `--enable` under a free name as the hint,
and `--ssh-host` allows a host. `--update` of an enabled one stays on the pin; `u` in
[the picker](#the-picker) moves it past the pin, asking first.

`--known` takes no other flag than the output flags, and it, `--enable`, `--add`, `--update` and
`--remove` are one action per run. `--enable` takes `-y`, `-n` and `--ssh-host`, not `--ref` or
`--path`: the pin and path are the entry's.

JSON of `--known`: `known`, an array (`[]` for none; one element for an id) of `id`, `source`,
`path`, `commit`, `curated`, `reason` and `enabled`; and `hints`. JSON of `--enable` is `--add`'s
(`markets`, `changes`, `updates`, `notices`, `dry_run`) with `known`, the entry enabled, as one
object.
