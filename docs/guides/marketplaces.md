# Marketplaces

A marketplace is a catalog of modules, each with the source it comes from. lola reads Claude
Code's `.claude-plugin/marketplace.json`, the documented format, and the Python lola's YAML
catalogs, so a marketplace you used there keeps working; both become one model. Your
marketplaces are yours alone and shared by every scope: `lola market` asks no scope, and scope
flags change nothing.

```sh
lola market acme --add https://github.com/acme/plugins       # fetch its catalog, register it as acme
lola market --add https://github.com/acme/plugins            # named by the catalog's own name
lola market --add https://raw.githubusercontent.com/acme/market/main/general.yml   # a catalog file
lola market mine --add ./my-marketplace                      # a folder you are authoring
lola market                                                  # list them
lola market acme                                             # its source, pin, entries and skips
lola market acme --update                                    # fetch it again, report what changed
lola market --update --all                                   # every marketplace
lola market acme --remove                                    # forget it; asks (-y)
```

- **Adding.** `lola market [<name>] --add <source>` (alias `--get`) fetches the source, your own
  argument, with a module source's rules: a git repository (`https://`, `ssh://`, `user@host:path`,
  at `--ref`), an `https://` archive, an `https://` URL whose path ends in `.json`, `.yml` or
  `.yaml` (the catalog file itself, at most 8 MiB; `http://` only on a loopback host), or a local
  folder. A catalog file on your disk is refused with the command that adds the folder holding it
  (`lola market --add <folder> --path .claude-plugin/marketplace.json`), and a local archive is
  refused: extract it and add the folder. A tree is fetched whole into lola's staging, never into
  your cache (a marketplace is not a module), and a symbolic link anywhere in it is refused. lola
  then looks for `.claude-plugin/marketplace.json`, `<name>.yml`, `<name>.yaml`, `marketplace.yml`
  and `marketplace.yaml` at its root, or in the directory `--path` names (`--path` may name the
  catalog file itself); two of them are refused with one `--path` command per file. The marketplace
  is named as you name it, else by the catalog's own `name`; for a catalog name that cannot name a
  marketplace (the Python lola's `Lola General Marketplace`) a terminal asks for a name, pre-filled
  with a free slug of it (`lola-general-marketplace`), and off a terminal (or with `-n`) it is
  refused with the command that adds it under that slug (`lola market lola-general-marketplace
  --add <source> --path marketplace.yml`); either way it is `lola market <slug> --update` when that
  slug is already this marketplace. A name is the module-name rule, and none of the Python lola's
  `lola market` verbs (`add`, `ls`, `list`, `rm`, `remove`, `update`, `set`, `show`, `search`); a
  name that breaks the rule is refused saying what breaks it (`B and N are uppercase`, `' ' is not
  allowed`), with the `--add` of its lowercase form as the hint when that is a name no
  marketplace has. A name you already have
  is refused: for the same source, ref and catalog file, with `lola market <name> --update`; for
  another, a terminal asks for another name (pre-filled with the first free one, `acme-2`), and off
  a terminal the hint is the `--add` of that free name, never an `--update` of the other source.
  The slug hint and the free-name hint name the catalog file lola found with `--path`, since
  another name would look for its own `<name>.yml`. A catalog file at a URL takes neither `--ref`
  nor `--path`. Adding asks nothing else: a catalog
  installs and runs nothing, and you typed its source. `-n` fetches and reports, and writes nothing.
  It prints the marketplace and how many entries lola skips, with why, grouped (`⚠ 3 entries
  skipped: 2 npm sources, 1 command source → lola market acme`).
- **Updating.** `lola market <name>... --update` (aliases `--upgrade`, `--refresh`), or
  `--update --all`, fetches each again from the source, ref and catalog file it was added from,
  replaces its cached catalog and reports each entry added (`+`), removed (`-`) or changed (`~`,
  with the fields: `source`, `path`, `ref`, `commit`, `version`). The skipped entries are reported
  again only when they change (another entry skipped, or another reason). A marketplace that fails (offline,
  a catalog that no longer parses) keeps its cached catalog and is reported; the others go on, and
  the run then fails. Names and `--all` are exclusive.
- **Removing.** `lola market <name> --remove` (aliases `--rm`, `--delete`, `--del`, `--erase`)
  forgets a marketplace: its line in `markets.yaml` and its cached catalog. It asks first; `-y`
  confirms, and off a terminal without `-y` it is refused with the same command and `-y` as the
  hint, which answers that removal and no other question. There is no safety check and so no
  `--force`: nothing lola installs depends on a marketplace. A notice counts the entries of this
  project (the one found from here) and your user scope whose `market:` names it, from its source: they keep
  their real sources and stay installed, and `lola sync --update` follows the marketplace again
  once you add it back (the notice gives that `--add`). `-n` says what it would remove.
  `--json` gives `removed`, `notices` and `dry_run`.
- **The Python lola's verbs.** `lola market add`, `ls`, `list`, `rm`, `remove`, `update`, `set`,
  `show` and `search` are refused with lola's form (`lola market ls is the Python lola's form; in
  lola: lola market`), and the hint is that command when it would run:
  `lola market add acme <url>` → `lola market acme --add <url>` (keeping `--ref`, `--path`, `-n`),
  `ls` → `lola market`, `ls acme` and `show acme` → `lola market acme`, `rm acme` → `lola market
  acme --remove` (with `-y` only when you gave it), `update` → `lola market --update --all`, `update
  acme` → `lola market acme --update` (with `--all` too, both forms), `search fmt` → `lola search
  fmt`, `set --disable acme` → `lola market acme --remove` (never with `-y`), and `set --enable
  acme` says it is already active, as lola has no disabled state. A name you do not have gets its
  close names, with lola's form of the run for each (never with `-y`), then the listing, and a verb missing its operand a usage message alone. The Python lola's picker
  for `rm` and `set` without a name is not reproduced: `--remove` takes a name.
- **A name you do not have.** Showing, updating or removing a marketplace you do not have names
  your marketplaces close to it (`you have no marketplace acmee; close names: acme`), with the run
  for each as a hint (`lola market acme`, keeping `--update` and `-n`; a `--remove` hint never
  carries `-y`), then the listing.
- **Listing and showing.** `lola market` lists `NAME`, `FORMAT` (`claude` or `lola-yaml`),
  `ENTRIES`, `SKIPPED`, `FETCHED` (`2 days ago`, or `not fetched` with the `--update` that fetches
  it) and `SOURCE`; `lola market <name>` shows the source, ref, commit, catalog file, owner and
  description, every entry, by name, with its version, source kind, source, path, ref, commit and
  description, and the skipped entries grouped by reason, the most common first (`npm sources are
  not supported: … (2): npma, npmz`). Every line fits the terminal's width (else `$COLUMNS`; a
  pipe or a file without `$COLUMNS` gets everything whole): a name is never cut, and one over 32
  columns prints alone with the rest of its row on the line under it; an entry's version is cut
  with `…` to at most 16 columns, and its source to at most 40; its description fills the rest of the row, cut with `…`, or goes on
  its own line under the row when fewer than 20 columns are left; and other long lines wrap at
  spaces, a field's value staying in its column. A single word wider than the terminal (a long
  name, a URL) is never broken: it prints whole on its line, which is then wider than the terminal.
  On a terminal the whole listing is paged as `lola sync`'s diffs are ([Paging](sync-and-trust.md#sync-pins-and-trust)); `--plain` and
  `--json` keep every text whole. Neither fetches nor asks. `--json` gives
  `markets` (`name`, `source`, `ref`, `path`, `format`, `commit`, `fetched_at`, `entries`,
  `skipped`, `description`) and `notices`; one marketplace's document adds `file`, `owner`,
  `version`, `entries` (`name`, `description`, `version`, `tags`, `source`, `ref`, `path`,
  `commit`, `kind`, `install`, the address that installs it, `notices`, and `components` when its
  catalog lists them) and `skipped` (`name`, `reason`). `--add` and `--update` give
  `markets`, `changes` (`market`, `entry`, `change`, `fields`), `updates` (`market`, `outcome`:
  `added`, `would_add`, `updated`, `would_update`, `up_to_date`, `failed` or `skipped`, `error`,
  `hints`), `notices` and `dry_run`.
- **What a catalog may name.** A catalog is a stranger's input, read entry by entry: an entry lola
  cannot use is skipped with its reason and the rest load. Skipped are `npm` sources (they run a
  package manager), `command` sources (they run a program), any other kind lola does not know,
  `ssh://` and `user@host:path` sources (they would use your ssh keys on a stranger's host), any
  source that is not `https://` at a host given by name (a path, `file://`, `http://`, `localhost`
  or an address inside your network, judged from the text alone, never looked up on your disk),
  entries that declare their `hooks`, `outputStyles`, `themes`, `mcpServers`, `lspServers`,
  `userConfig` or `channels` in the catalog (lola reads a plugin's components from its own files),
  entries whose source is another repository and that list their `skills`, `commands` or `agents`
  (the catalog does not own that repository), entries with `headers` or a `headersHelper`, a `sha` that is not a
  full commit or disagrees with a full-commit `ref`, a `sha256` that is not 64 hex digits, a name
  that fails the module-name rule, two entries of one name (both), a key given twice or in another
  case (`Source`), a description or source over 4096 bytes, more than 64 tags, and skills,
  commands and agents lists naming more than 1024 paths. A
  relative entry (`"./tools/formatter"`, or a bare name under `metadata.pluginRoot`) must stay in
  the marketplace (no `..`, no backslash, never absolute) and be a directory there; it comes from
  the marketplace's own source, at its commit, and a marketplace fetched as a single catalog file
  has none. A relative entry may list its `skills`, `commands` and `agents` (a path or a list of
  paths in its directory, as Anthropic's `anthropics/skills` marketplace does): the catalog's
  author owns that tree, so the lists say which of its files form the module. Each path must stay
  in the entry's directory (no `..`, no backslash, never absolute, never the directory itself),
  two paths of one list never share a name, and the fetch must find each as a real directory for
  a skill, a real file or directory otherwise, else the entry is skipped; `lola market <name>`
  shows what each entry selects. A Python entry needs `name` and `repository`; its `pre-install` and `post-install`
  hooks are never run (`catalog install scripts are ignored`). A catalog over 8 MiB, with more than
  4096 entries, nested deeper than 64, with a top-level key given twice or in another case, or with
  a control or invisible character in any key or field lola reads, is refused whole, naming the
  field (`plugins[12].description`).
- **Installing.** `lola install @acme/formatter` installs the entry `formatter` of your
  marketplace `acme`, as its cached catalog lists it (`lola market acme --update` fetches it
  again); `lola cache add @acme/formatter` only caches it and `lola check @acme/formatter` only
  checks it. `@market/module` is command-line input only: no file lola reads takes it as a
  `source:`, so what a file installs never depends on the reader's marketplaces. A `project.yml`,
  `user.yml` or profile (yours, `default`, a project's or a remote one) that has one is refused by
  every command that reads it, `lola list` included, before any trust question or network contact,
  naming the file, the line, the module and the address (`.lola/project.yml:14: formatter's source
  is @acme/formatter, a marketplace address; a file must record the module's real source`). When
  you have that marketplace, `project.yml` and `user.yml` get one hint per such entry, `lola
  install --project @acme/formatter` (`--user`, or `-C <root>`, as the file's scope; with `-n` and
  the output flags when the run had them), which shows where the address resolves before
  anything is asked and replaces that entry with what it resolved; the other such entries stay as
  written, each named in a warning with its own hint, and the file is refused until none is left.
  A profile, or an entry not named after the module its address installs, gets `lola market
  acme` instead, which shows the entry's source, ref and path to write there. Without the
  marketplace there is no hint, and the message says to edit the entry to name the module's
  source, ref and path. A remote profile with one is refused at `--add` and `--update`. Before anything is asked, the install shows where the address resolved
  (`→ @acme/formatter → https://github.com/acme/monorepo.git path tools/formatter @ v2.0.0
  (a1b2c3…)`). What lands in `project.yml` (or `user.yml`) is the module's real source, `path`,
  the catalog's `ref`, and the pin, plus `market:`:

  ```yaml
    - name: formatter
      source: https://github.com/acme/monorepo.git
      path: tools/formatter
      ref: v2.0.0
      commit: a1b2c3…        # the catalog's sha, or the head lola resolved
      digest: sha256:…       # always computed by lola from the fetched content
      market:
        name: acme
        source: https://github.com/acme/plugins
  ```

  The module is named after the entry: a module that does not name itself (no `lola.yml`
  `name`) takes the entry's name, here and wherever the entry is fetched again (a teammate's
  sync, `lola cache update`); one that names itself otherwise is refused. A relative entry pins
  the marketplace's own commit. A catalog `sha`, or a `ref` that is a full commit SHA, must be
  reachable from the entry's `ref` (the default branch when it gives none) before anything is
  cached, though Claude Code would install it: `marketplace acme lists formatter at a1b2c3…, which
  v2.0.0 does not reach` → `lola market acme --update`. An archive's `sha256` must match the
  download before anything is extracted. A source the catalog names follows no redirect to a host
  lola would refuse (after install, `render`, `lola cache update`, an install by cached name that
  fetches again and a `--ref` install fetch under the rules of a source you type; see
  [the security model](../architecture.md#security-model)). `--ref` replaces the catalog's ref and
  drops its pin; the entry then records no `market:` (`formatter: pinned to your ref v3; not following acme`). `--path` is refused: the
  catalog gives the path. The install says what lola does not do for the entry: a Claude entry's
  `dependencies` are never installed (each one the same marketplace lists gets its own
  `lola install @acme/<name>`), and a Python entry's install scripts never run. An address lola
  cannot resolve is refused with what there is: `you have no marketplace acme` (naming your close
  ones) → `lola market`, an
  entry the catalog does not list with its close names, or one it skips with why →
  `lola market acme`. An entry whose catalog lists its skills, commands or agents installs exactly
  those, each as `skills/<name>`, `commands/<name>` or `agents/<name>`, and the entry records the
  selection as `components: {skills: [skills/xlsx], commands: [...], agents: [...]}`, beside its
  source; `lock.json` and your cache record it too, so a teammate's sync, `render` and
  `lola cache update` fetch the same selection without the marketplace. A module that names itself
  otherwise in its `lola.yml` is refused, naming both names. When the entry's source is the
  marketplace's own (a relative entry), the hint installs that source under the module's own
  name, at the commit the catalog pins and with your flags, and alone when the run named other
  modules too, since `--ref` and `--path` apply to one source (`lola install <source> --ref <commit>
  --path <path> …`; `lola cache add …` for `cache add`, and `lola check <source>` for `check` when
  it needs no `--ref` or `--path`). There is none for a source the catalog names, as one you type
  would not keep the catalog's redirect rule or an archive's `sha256` (the message says which),
  nor for a selection, which no install by source can make.
- **Browsing.** `lola install @acme` on a terminal offers acme's entries as the module picker does
  (pick any); off a terminal it is refused, naming its first ten entries as addresses, with
  `lola market acme` as the hint. `lola install` with no argument offers every marketplace as a
  group after your cache ([The install flow](../usage.md#the-install-flow)). A bare name your cache does not
  hold, and that is no source here, but that your catalogs list, is never picked for you: on a
  terminal lola asks which marketplace's entry you mean (marketplace, version, description,
  source host); off one it is refused with your run once per combination of the marketplaces
  listing each such name, every name replaced by an address, keeping your flags but `--path`
  (with more than ten combinations, none: name each as `@<market>/<name>`). `lola search` finds catalog entries too, each with
  its `install` value. Shell completion completes `@acme` and `@acme/<entry>` from your fetched
  catalogs. An entry whose cache record lola cannot read is offered as not cached, with a warning
  naming the `lola cache rm` that clears it (completion stays silent). After `--add`, lola prints `→ browse: lola install @acme`.
- **Provenance.** An entry of `project.yml` or `user.yml` may carry `market: {name, source}`, the
  marketplace it was installed through. It is provenance only: the entry's real `source`, `ref`,
  `path` and pin are what lola fetches, so a teammate who has never heard of that marketplace
  syncs it as any other entry. lola checks that the name is a marketplace name and the source a
  source lola could record, and shows both as written (`lola list --json` gives `market`). A
  profile never carries it: `market:` in a profile is refused as an unknown key, naming the line.
  `lola list` shows it, `lola market <name> --remove` counts it, every `lola sync` fetches an
  entry whose `market` names another source than its own under the catalog's redirect rule, and
  `lola sync --update` follows that marketplace's catalog only when your marketplace of that name
  has the source the entry records, and only within the same source, path and ref
  ([Sync, pins and trust](sync-and-trust.md#sync-pins-and-trust)); nothing else reads it. A file someone else wrote
  can therefore move an entry only to a new commit your own catalog lists at the same source,
  path and ref.
- **Hints.** Every hint `lola market` prints is a command that runs as shown, and keeps the
  run's `--json`, `--plain` and `--no-pager`.
- **Folder marketplaces.** A folder you are authoring is recorded by its absolute path, links
  resolved, and cached like any other: your edits reach lola only through
  `lola market <name> --update`. Its relative entries are folder sources, unpinned.
- **Where they live.** `$XDG_CONFIG_HOME/lola/markets.yaml` lists your marketplaces (`name`,
  `source`, `ref`, and `path`, the catalog file found at `--add`); it is yours to edit, and is read
  strictly (a folder must be an absolute path; a mistake is refused naming the line). Each fetched
  catalog is cached byte for byte in `$XDG_CACHE_HOME/lola/markets/<name>/` with `source.json`,
  its pin (source, ref, path, file, commit, digest, when it was fetched, and the directory the
  marketplace's files were found in when it is not the source's top, such as an archive's
  `repo-<sha>/`, so its relative entries install from there): a wiped cache loses
  nothing you chose and costs a `lola market --update --all`. A cached catalog is written before
  its registration, so a failed write leaves at worst a cache directory that listings ignore.
