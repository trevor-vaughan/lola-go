# Marketplaces

Maintainer notes on Marketplaces. It is split out of [architecture](../architecture.md).

`lola market` adapts `internal/engine/market.go`; the catalogs themselves are read
by the pure `market` package (see [Packages](#packages)), and every file and network access is
the engine's.

- **Storage**.
  - `markets.yaml` (`paths.Markets`, your file, read through `yamldoc.Read` and
    `market.ParseRegistry`, strict, following a symbolic link as `config.yaml` does, and edited by
    `market.AddRegistration`/`RemoveRegistration`, keeping comments) lists your marketplaces.
  - Each fetched catalog is cached in `paths.MarketCache/<name>/`, `catalog.json` or
    `catalog.yml` byte for byte and `source.json` (`market.Pin`).
  - The cache is written by `writeDir` (the remote profiles' crash-safe directory swap: a `.new-`
    directory renamed into place, the old one renamed aside first).
  - It is read by `loadMarket`, which refuses a link, a pin that is not lola's or does not name
    the registration's source, ref and path, and a catalog whose digest is not the pin's.
- **The markets lock** (`paths.MarketsLock`) is taken after the profiles lock and before every
  other.
  - The order is profiles → markets → selections → scope → cache → trust; `lola tidy` takes
    `keep.lock` (`paths.KeepLock`) after the scope lock, around keep.yaml's read-modify-write.
  - `install` (with an address) and `sync --update` (with an entry that has `market`) hold it
    shared from after the profiles lock to the end of the run.
  - `readMarkets` takes it shared (through `readSwept`, which, as for remote profiles, sweeps a
    killed writer's `.new-`, `.old-` and `.del-` directories under it exclusively first), and
    takes none for a read-only or dry run when no lola has made the lock file.
  - Writers take it exclusively only to check the registration again and write.
- **`AddMarket`**.
  - Before it contacts the network, it checks the name and the typed source (`marketSource`: a
    catalog-file URL by `market.IsCatalogURL`; otherwise `source.Parse` with the module rules, so
    a folder is made absolute with its links resolved; a local catalog file or archive is
    refused, coached to the folder that holds it; `source.Recorded.Check` and `source.Validate`)
    and a typed name that is already this marketplace (`sameMarket`).
  - After the fetch, a name another marketplace has (typed or the catalog's) goes to
    `takenName`: `Rename` asks a free name on a terminal (`askName`, which asks again until the
    answer is a valid name no marketplace has, pre-filled with the refused answer as typed), else the hint is `market.FreeName`'s `--add`.
  - A catalog's own name that no marketplace can have is asked the same way, pre-filled with its
    free slug, else refused with the slug's `--add`.
  - Those hints name the catalog file found with `--path` for a tree source, as another name
    would look for its own `<name>.yml`.
  - A typed name that breaks the rule is refused with `module.ValidateName`'s account of what
    breaks it, and the `--add` of its lowercase form when that is a name.
  - `noMarket` refuses a marketplace you do not have, naming `CloseMarkets` and hinting the
    caller's run with each (`ShowMarket`, `UpdateMarkets`, `RemoveMarket`, never `-y`).
  - `fetchMarket` downloads a catalog file (`source.FetchFile`, 8 MiB, the source URL rules, and
    the archive download's redirect rules) or fetches the tree whole with `source.Fetch` into the
    run's staging, never through the cache. It then:
    - finds the catalog (`catalogIn`: `--path` as a file or a directory, else
      `market.CatalogFiles`; `realPath` refuses a link on the way);
    - reads it with `fsx.ReadOwn` and parses it (`market.Parse`);
    - records in `Pin.Absent` each relative entry whose directory the tree does not hold, and
      each path a content list selects that is not there as its kind needs;
    - binds it (`market.Bind`).
  - Then, under the exclusive lock, with the name checked again, it writes the cache, then the
    registration, so a failed registration leaves a cache directory that listings, driven by
    `markets.yaml`, ignore, and the next `--add` replaces.
- **`UpdateMarkets`** checks every name before any network contact.
  - It then fetches each registration at its source, ref and recorded catalog file
    (`updateMarket`), replacing its cache under the exclusive lock.
  - A failure is that marketplace's outcome and keeps its cache, and the CLI fails the run once
    the others are written.
  - `marketChanges` compares the old and new models by entry name.
  - `Markets` and `ShowMarket` only read; a cache that cannot be read lists as not fetched with a
    notice naming `--update`.
  - Errors from the source, file and lock layers lose their prose hints on the way out
    (`unhinted`), as a marketplace's hints are commands only; their messages still say what
    failed.
- **`RemoveMarket`** asks (a nil `Confirm` refuses, hinting the same run with `-y`, which approves
  exactly that removal; no other hint adds `-y`) under the shared lock, then, under the exclusive
  one, writes `markets.yaml` without the marketplace and removes its cache, renamed aside to a
  `.del-` directory first.
- **Resolution**.
  - `resolveMarkets` resolves every `@<market>/<entry>` argument of an install, `cache add`
    (`AddEntry`) or `check` (`checkEntry`) into its `InstallRequest.market` (`marketPick`), under
    the shared markets lock, which an install takes after the profiles lock and before selections
    and scope.
  - It reads the registration and the cached catalog, never the network, and refuses a
    marketplace you lack or have not fetched, an entry skipped or not listed
    (`market.CloseNames`), a bare `@<market>` and `--path`, before anything is fetched.
  - `resolve` hands such a request to `resolveEntry`: the entry's `Spec` at the catalog's ref (or
    `--ref`, which drops the commit pin and the `market:` provenance), recorded with
    `market: {name, source}` from your registration and, for an entry whose catalog lists its
    skills, commands and agents, `components:`.
- **Selections** (user decision, 2026-10-22). `source.Components` is a catalog's selection of a
  relative entry's directory.
  - `Spec.Components` makes `source.Fetch` fetch the whole directory (its own root, never a
    `module/` in it) and keep only those paths, each moved to `<kind>/<its last element>` with its
    execute bits (`selectComponents`).
  - `Components.Check` refuses a path that is not clean and relative, two of one name,
    overlapping ones, or none.
  - The selection is recorded wherever a fetch is rebuilt from:
    - `manifest.Entry.Components` (`project.yml` and profile entries other than a remote profile's,
      as for `market`);
    - `ledger.Entry.Components` (`lock.json`);
    - `store.Origin.Components` (the cache, whose `Commit` refuses the name held with another
      selection, and which `cache update` and an install by cached name read);
    - `trust.Entry.Components` (omitempty, so earlier hashes stand; shown in the diff through
      `untrusted.Text`).
  - So a sync, update or cache update of the entry stages the same module, with the same digest,
    without the marketplace.
  - `sync --update` follows a catalog only while it selects the same components.
- **Pins**.
  - `fetchEntry` checks a commit pin with `source.CheckReachable` against the spec carrying the
    catalog's ref, never the commit (so `reach.go`'s `spec.Ref == commit` shortcut cannot apply),
    before anything is fetched, then fetches by the commit and refuses another.
  - `source.Spec.SHA256` and `FromCatalog` carry the archive sum and the redirect rule.
  - `cacheEntry` prepares it under the entry's name (`PutRequest.Name` and `FallbackName`: a
    module that names itself otherwise is refused, one that does not takes the entry's name) and
    commits it with `headIfAbsent` when pinned.
  - Every later fetch of an entry by its recorded name (`prepareFetch`, `fetchPin`, `fetchAs` for
    a cached name, `cache update`) falls back to that name too, so a teammate without the
    marketplace gets the same module and digest.
  - The install shows each resolved address through `Options.ShowResolved` before any consent,
    and adds `entryNotices` (the `--ref` notice, the catalog's own notices) once and
    `dependencyNotices` (one install per dependency the same marketplace offers) per scope.
- **Browsing**.
  - `MarketGroups` is every fetched catalog's installable entries (`MarketOffer`: the address, the
    source host, and whether the cache holds the name from the same source), read under the
    shared lock, never fetched.
  - `InstallChoices` carries them as `Choices.Markets` for the module picker's groups.
  - `MarketMeaning` is what a bare name may mean (only one your cache lacks and that is no source
    here).
  - `Search` (with markets, for `lola search`) appends the matching offers after the cache's hits,
    as `SearchHit.Where` `@<market>` with `Install`.
  - The CLI's install flow turns a picker's answer into arguments (`expandMarkets`: `@<market>` on
    a terminal, a bare name your cache lacks that a catalog lists, all of a run's refused at once
    off a terminal), so the summary's equivalent commands name the addresses.
  - A picked entry is fetched through `InstallChoices` like any argument, and its version (with
    the commit it was fetched at, `VersionRef.commit`) pins the install.
  - `RemoveMarket` counts the entries of the known scopes whose `market` names it
    (`installedFrom`).
- **`@` in files**.
  - `manifest.Address` recognises a `source` of the form `@<market>/<module>` (both parts valid
    names; anything else starting with `@` is a path, as before).
  - `manifest.Load` and the profile loaders (`profile.Load`, `LoadProject`, `LoadRemote`,
    `Fetched`) check every other rule first, then report all such entries in one
    `*manifest.UnresolvedError` (the file, and each entry's name, address and source line), with
    no hints, since those depend on the reader.
  - Every reader of a manifest or profile refuses it before its trust check and any network
    contact: `load` reads the manifest before the trust record, and `fetchCatalogs`
    (`sync --update`) reads it, and the profiles the run would read, before any catalog is
    fetched.
  - `CoachUnresolved`, deferred in `InstallAll`, `Sync`, `Uninstall`, `Status`,
    `AddProfile` and the update's `fetch`, and in `InstallChoices`, adds the hints from your
    `markets.yaml` (`unresolvedHint`):
    - for an entry whose marketplace you have, `lola install @m/x` at the scope of the manifest it
      is in (matched by path among the run's scopes), with `-n` and the output flags, when the
      entry is named `x`;
    - else `lola market <m>` with the output flags (a profile, or an entry named otherwise, which
      that install would not replace);
    - none without the marketplace, the message then saying to edit the entry
      (`Unresolved.Coached` drops that text for an entry with a hint).
  - An unreadable `markets.yaml` gives no hint.
  - `ProfileState` (`lola profile <name>`), `UpdateProfile`, `RemoveProfile`, `market --remove`'s
    count (its warning, after `→`) and migrate's plan (`NewPlan`, with the run's output flags)
    coach the same way; an error a reader already coached is left as it is.
  - An install replaces: `replacingNames` are the entries its addresses install, and
    `manifestReplacing`, through `loadReplacing` in `InstallAll` and in the pickers' reads (and
    `InstallManifest` for the CLI's target lookup), reads a manifest whose unresolved entries
    include one of them as written (`UnresolvedError.Manifest`), returning the others, which
    `unresolvedNotices` warns of, each with its own hint.
  - The entry is then written as any install's is (`planInstallModule`: a covered entry keeps its
    targets and rules), and `manifest.Edit` keeps every other unresolved entry as written; the
    trust refresh covers only what the install wrote.
  - The pickers' uncovered-entry notice leaves such entries out. `readd` leaves such a refusal of
    a kept remote profile as it is, since adding it again would refuse again.
- **`sync --update`**.
  - `fetchCatalogs` reads the manifest once without the scope lock to name the marketplaces its
    entries of the run came through, and fetches each one whose registration has the recorded
    `market.source` (`sameMarketSource`) once (`updateMarket`, which writes the cache unless dry),
    under the markets lock held from after the profiles lock until the sync ends; a failed fetch
    is a warning.
  - `validateSync` asks `follow` per entry with `market`: it follows the catalog's entry only at
    the same source, path and ref, giving the fetch the catalog's sum and redirect rule.
  - Any other case is a notice, with `lola install @m/x` when the catalog moved the entry's ref
    and none for a source, path or selection (the cache would refuse that install while it holds
    the name), and the entry updates as a plain one.
  - In every sync, an entry whose `market` names another source than its own is fetched with
    `FromCatalog`; `cache update` fetches from records without `market` and
    keep today's rule (residual).
  - `resolveSync` then takes the catalog's commit pin through `followPin` (`fetchEntry`'s
    reachability check, a fetch by the commit, `headIfAbsent`).
- **`CoachMarketVerb`** refuses the Python lola's verbs, which the CLI routes to it whenever
  the first argument is one (`market.Verbs`); the translation is a hint only when it would run:
  a name you do not have gets the listing, one that cannot be a marketplace and a missing
  operand get none. The hidden `--enable` and `--disable` flags exist only for `set`.
- **Hints** are bare commands (`MarketRun`, quoted by `errs.ShellJoin`, every argument through
  `untrusted.Text`), carrying the run's output flags (`--json`, `--plain`, `--no-pager`, as lola
  profile's hints do; `Display`) and exactly these other flags:
  - `--ref`, `--path` and `-n` for the `--add` hints of a slug, of a collision and of two
    catalogs;
  - `-n` for the `--update` hint of the same source;
  - `--ref`, `--path`, `-n` and (for `rm` and `remove`, when typed) `-y` for the verb
    translations;
  - `-y` for `--remove`'s own refusal off a terminal;
  - none for the listing and `--update` hints.
  - A refusal with no working command has no hint, and its message says what to do.
