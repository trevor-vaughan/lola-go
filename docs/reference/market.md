# `lola market`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola market [<name> [<entry>] | [<name>] --add <source> [--ref <ref>] [--path <file-or-dir>] | <name>... --update | --update --all | <name> --remove | --known [<id>] | [<name>] --enable <id>]
```

Aliases: `marketplace`, `markets`. Five read-only paths, none of which asks:

- `lola market` lists your [marketplaces](../guides/marketplaces.md#marketplaces): format, entries,
  skipped entries, when each was fetched and its source.
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
`version`, `repository`, `ref`, `path` and `tags`; `ref` is the branch, tag or commit of a git
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
- A source where the name goes (`lola market --add acme https://github.com/acme/plugins`): the
  source is refused as a marketplace name, hinting the run with the two swapped (`lola market acme
  --add https://github.com/acme/plugins`).

JSON:

- Listing prints `markets` (one row per marketplace) and `notices`.
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
and `--ssh-host` allows a host. `--update` of an enabled one stays on the pin.

`--known` takes no other flag than the output flags, and it, `--enable`, `--add`, `--update` and
`--remove` are one action per run. `--enable` takes `-y`, `-n` and `--ssh-host`, not `--ref` or
`--path`: the pin and path are the entry's.

JSON of `--known`: `known`, an array (`[]` for none; one element for an id) of `id`, `source`,
`path`, `commit`, `curated`, `reason` and `enabled`; and `hints`. JSON of `--enable` is `--add`'s
(`markets`, `changes`, `updates`, `notices`, `dry_run`) with `known`, the entry enabled, as one
object.
