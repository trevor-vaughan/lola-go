# Packages

Maintainer notes on Packages. It is split out of [architecture](../architecture.md).

- `paths`: XDG resolution from an injected `getenv`; no globals. `ProjectKey`, the canonical form of
  a project directory that the project lock and `keep.yaml` use, and `selections.yaml` for a
  project outside a clone.
- `config`: `config.yaml`, `selections.yaml` and `keep.yaml` (`lola tidy`'s keep-list: a `user`
  list and per-project lists of selectors, project keys canonical path keys), and `RepoKey`, a
  project's repository key in `selections.yaml`:
  strict loading of untrusted, hand-edited YAML, selectors, effective exclusions, and locked
  node-preserving writes.
- `yamldoc`: Reading a hand-edited YAML file as one untrusted document.
  - `Read`: non-blocking open, regular files only, size cap, one document, no anchors,
    aliases or merge keys.
  - The node helpers `config` and `manifest` edit with.
  - `ReadNoFollow` reads a file someone else committed: never through a symbolic link
    (`O_NOFOLLOW` where the system has it, and on every system the file opened must be the one
    `os.Lstat` finds).
  - `Decode` reads bytes the caller has already capped (a marketplace catalog) with the same
    guards, plus a refusal of any mapping key given twice, which yaml.v3 refuses only when it
    decodes into a Go value; it applies no version rule.
- `fsx`: Atomic writes, reading a file lola wrote without following a link (`ReadOwn`),
  symlink-aware destination resolution, comment-preserving JSONC edits (object members, and array
  elements of any JSON value), mode normalization. `JSONDeleteMember` removes one member with its
  own comments (the lines directly above it, up to a blank line or the previous member) and nothing
  else: no parent is pruned, so an emptied server map stays; `JSONDelete` prunes
  empty parents for lola's own members.
- `module`: The IR, frontmatter parsing, name rules, format detection and the Claude plugin and
  Agent Plugins adapters, loading `lola.yml` (with `components`) + the native layout, hardened
  module JSON reading, rules, hook validation, and normalization.
- `templating`: The Liquid sandbox: `Render` and `RenderDoc` screen, compile and render one
  template and the partials it includes under caps, with lola's 41 filters; `Uses` tells which
  bindings a module's templates read, walking each partial at most once per module, and `AttrRefs`
  lists the literal `attrs` paths they read. It alone imports liquid (vendored in
  `third_party/liquid`) and reads no file: partials come in as `Partials`; depguard enforces both.
- `untrusted`: Makes text lola did not write safe to print: control characters, Unicode format
  characters and line separators replaced, length capped, OS errors without their path.
- `source`: Source parsing and validation.
  - Parsing and validation also cover sources recorded in `project.yml` and `lock.json`
    (`Recorded`).
  - The folder, git and archive fetchers with their executables, and the reachability check of a
    pinned commit.
  - `GitCommand`, the one hardened way lola runs git (also for the engine's git-ignore check and
    migrate's git-tracked guard, with `InWorkTree`).
  - `CheckRemote` is the address rule for sources a stranger's file names (a remote profile and
    its entries; it lives here so a package that checks such sources need not import `profile`).
    It reads the text alone, never the disk.
    - It takes `https://`, `ssh://` or git's scp form, at a host given by name.
    - It never takes a path or `file://` URL, a URL that does not parse, plain `http://`,
      `localhost` or `*.localhost` (trailing dots included), a loopback, link-local, unspecified,
      private, carrier-grade NAT or site-local (`fec0::/10`) IP address, NAT64's local-use
      `64:ff9b:1::/48`, an IPv6 address embedding such an IPv4 one (`::a.b.c.d`,
      `64:ff9b::/96`), or an IP address in any but its canonical form (a host whose last label is
      a number, WHATWG's "ends in a number", is an IP address to git and ssh).
  - A `Spec` a marketplace's catalog named (`FromCatalog`) keeps the rule
    past the first request: every redirect of its archive download must pass `CheckRemote`, and
    git runs with `-c http.followRedirects=false` (`remoteArgs`) for its `ls-remote`, clone and
    fetches, the reachability check's included. A redirect git refuses so is reported as `<url>
    redirects, and lola follows no redirect of a source a catalog names` (`refusedRedirect`).
  - `Spec.SHA256`, a catalog archive's listed sum, is checked against the downloaded bytes before
    anything is extracted (`SHA256MismatchError`).
- `store`: The cache's storage. `store` is the package's internal name; user-facing text says
  "cache". Content-addressed versions with their executables, per-ref heads with their commits,
  `Prepare` (check and digest in the run's staging) and `Commit` (publish under the cache lock;
  `Put` is both), snapshots of a pinned version (`Pinned`), the cache lock.
- `ledger`, `manifest`: Observed and desired state, validated at load; the ledger written
  deterministically, the manifest edited in place (`manifest.Edit`) with its pins.
  - `ledger.Strays` lists what lola could not have written: artifact and `created` paths that
    are not `filepath.IsLocal`, and, given the targets the caller knows, items on any other
    target. `Load` refuses the first path (`*ledger.StrayError`); `LoadKeepingStrays` reads it,
    keeping every other refusal and its `restoreHint`; `Forget` drops strays and any module then
    installed on no target (an entry from before `targets` was recorded keeps its artifacts'
    targets).
- `profile`: Profiles. No network, no writes; depguard holds its imports (`source`
  for the pin and address rules only, `config` for the selector grammar of an entry's `only:` and
  `skip:` only, never a file).
  - `Visible` finds the user's profiles and, at user scope, `default.yml` by their names alone
    (at most 256 entries in `profiles/`), and counts the `.yml` files whose names fail the
    module-name rules, which listings report as a count and never by name.
  - `Load` reads one through `yamldoc.Read` (following a symbolic link, as for `default.yml`),
    version first, known keys only, at most 512 entries each checked as a manifest entry
    (`manifest.Entry.Validate`), every refusal with the file and line.
  - Entries decode into `entry`: a `manifest.Entry`'s keys plus `market`, `components` and the
    picks `only` and `skip` (each a `kind/name` selector, checked by `checkPicks` through
    `config.ParsePattern`). A remote profile's entry takes none of the four;
    any other key is refused as unknown. `Entries` gives entries without targets the profile's
    `targets`.
  - `Edit(path, change)` returns the bytes of the file `Load` read, edited by
    `manifest.EditDocument` (comments and order kept), and where to write them; it never writes.
    The caller calls `Current` (and `Unchanged` for a project profile) just before, so a file that
    changed since the run read it is refused.
  - `Project` lists a project's committed profiles in `<root>/.lola/profiles/` the same way,
    after refusing a `.lola/` or `.lola/profiles/` that is a symbolic link, not a directory, or
    resolves outside the root (`fsx.Resolve`). A `default.yml` there is no
    profile, and its path is returned for listings to warn about (a project cannot define
    `default`).
  - `LoadProject` reads one as `Load` does but refuses a symbolic link and opens it with
    `yamldoc.ReadNoFollow`, coaching "restore it from version control".
  - `Remote` lists the remote profiles in `$XDG_DATA_HOME/lola/profiles/`, one directory each (a
    name starting with `.` is a directory lola is writing, never a profile).
  - `LoadRemote` reads `<name>/profile.yml` and `source.json` (`Origin`: version, source, ref,
    path, commit, digest) without following a link, checks the origin (`Origin.Check`: the
    recorded-source rules, a remote source, a full pin) and that the file's bytes have the digest
    it records, then applies a remote profile's rules: every entry names a remote source and
    carries its pin, a remote source being one `source.CheckRemote` takes.
  - `Fetched` and `FetchedNames` read `<name>.yml` from the `profiles/` directory of a source the
    engine fetched into staging, refusing a `profiles/` or a file that is a symbolic link before
    reading, with the same rules.
- `market`: Marketplaces.
  - Pure: no file system and no network. depguard enforces this by denying it `os` and `io/fs`,
    and forbidigo by denying it the functions of `source`, `yamldoc`, `manifest` and `module`
    that reach the disk, the network or git, or load and write modules and manifests
    (`source.Parse`, `Fetch`, `yamldoc.Read`, `manifest.Load`, `module.Load`, …), so no catalog
    string can reach the disk from it.
  - `Parse` reads a catalog in Claude Code's `marketplace.json` format (decoded token by token,
    so every object keeps its keys in order: a key given twice, or a case variant of one lola
    reads or skips on, is ambiguous, refusing the catalog at its top level and skipping the entry
    inside one; nesting at most 64) or the Python lola's YAML (`yamldoc.Decode`) into one model,
    `Catalog`.
  - Before that it refuses more than 8 MiB, more than 4096 entries, a catalog name, owner or
    version over 256 bytes or description over 1024, and any key or read string that
    `untrusted.IsPlain` refuses, naming the field. What decoding allocates is bounded too, as the
    byte cap alone does not bound it: at most 262144 JSON values, and a YAML catalog at most
    512 KiB and 131072 nodes.
  - Each entry is adapted on its own, and one lola cannot use is a `Skip` with its reason: a
    source kind lola does not take (npm, command, any other), component fields beyond the
    content lists (and those on any entry but a relative one), headers and headersHelper, a
    malformed or disagreeing `sha` or `sha256`, an oversized field (a version over 256 bytes or a
    description over 4096 among them), a name that fails the module-name rule or that two entries
    share.
  - Every network source a catalog names is classified from its text alone (`remoteSource`):
    only `https://`, or ssh and scp form at a host in the marketplace's `ssh_hosts`
    (`market.SSHHost`, `CheckSSHHost`), then `source.CheckRemote`, `source.Recorded.Check`
    and `source.Validate`; a full-SHA ref becomes the commit pin.
  - Relative entries keep the directory they name (`Entry.Relative`, checked by
    `CheckRelative`), which `Bind` maps to the marketplace's own source, ref and commit from its
    pin (`Pin`, `source.json`), under `Pin.Root` (where the fetched tree was in the source,
    `source.Fetched.Root`: an archive's wrapping directory or a `module/`, `""` for the top), so
    the entry's path is a `--path` that reaches it; skipping those of a single-file marketplace
    and those the fetch did not find (`Pin.Absent`).
  - A relative entry's content lists, `skills`, `commands` and `agents`, become
    `Entry.Components`, paths in its directory checked as relative ones are (at most
    `source.MaxComponents`, 1024, which `Components.Check` also enforces on every file that
    records a selection, checking overlap with a set in O(n·depth)). `Selected` names each in the
    fetched tree for the engine to check (a skill a real directory, a command or an agent a real
    file or directory), and `Bind` skips an entry one of whose paths the fetch did not find.
  - `ParseAddress` reads `@<market>/<entry>` (or `@<market>`), both parts module names.
    `CloseNames` gives a missing entry's near names and `CloseMarkets` a missing marketplace's
    (`closeTo`: case-insensitive Levenshtein distance 2, or containing it; at most 10).
    `Entry.Spec` is where an entry is fetched from.
  - `ParseRegistry`, `AddRegistration` and `RemoveRegistration` read and edit `markets.yaml`,
    keeping comments; `ValidateName` is the module-name rule minus the Python verbs (`Verbs`);
    `Slug` and `FreeName` make the names hints offer; `CatalogFiles` is the lookup order of a
    marketplace's catalog.
- `activeprofile`: The qualified name of the active profile a scope's `lock.json` stores
  (`user:go`, `project:go`, `remote:go`; never `default`), checked by `Check`; `Max` (64) only
  bounds `rendered_with.profiles` (`default` aside): a leaf with no YAML, so
  `ledger` imports it and `profile` may import `ledger` later without a cycle.
- `trust`: The trust record (`trust.json`): per project key, the view of `project.yml` lola wrote
  and of each project profile, and its hash; strict, permission-checked reads, locked per-project
  saves, coverage, the per-field refresh, and `Check`: the status and the typed field changes of a
  manifest against its record. No dependency on `engine`.
- `consent`: Recorded consent to a module's install hooks (key `<module>`) and runtime hooks (key
  `<module>/runtime-hooks/<settings-file hash>`), each recording a digest.
- `target`: The target contract: `Target`, `PlanContext`, `Finding`, capability tables and `Admit`,
  `Probe`, and helpers renderers share. No implementations.
- `target/claude`, `target/gemini`, `target/opencode`, `target/agents`: The `claude-code`,
  `gemini-cli`, `opencode` and `agents` renderers, each with its capability table and probe. One
  package per target.
  - `agents` writes skills only (capability table: `skill`, `Open`, a `description` required, the
    agentskills.io name pattern), has no binary, and is set up where `.agents/skills/` exists.
  - `PlanContext.SharedSkills` carries the skills `agents` delivers, so `gemini` and `opencode` skip
    their own copy (`skill-via-agents`); the engine fills it by planning `agents` first, and `check`
    does the same.
  - `engine.TargetChoice.SetUpOnly` holds targets with no binary whose marker exists; `Installed`
    and `SetUp` stay binaries on `PATH`.
- `target/conformance`: Specs every registered target must pass.
- `targets`: The registry: the one list of every target.
- `plan`: Intents, `Compute` (diff + conflicts) and `Apply` (transaction over files, ledger and
  manifest).
- `engine`:
  - Install, uninstall, sync (which `lola update` runs installed-only) (`desired`, the trust report,
    containment, removals), tidy, status, fetch, update, search, `Info` (lola info: a version's
    view, `ModuleView`, which cache add and cache list print too, the code it runs, and install's
    dry run pinned to it, with the consent it meets).
  - Run staging, module copies, install hooks, runtime hook consent, the check that local
    settings are git-ignored and the check that the ledger is not tracked by git.
  - Every manifest write with its trust refresh (`saveManifest`) and the refusals of untrusted
    entries.
  - Resolving an install's scope, the component resolver, selection edits and `InstallChoices`.
- `migrate`: `lola migrate`: strict reading of the Python lola's `installed.yml` and `source.yml`,
  the plan (destinations, outcomes, recorded paths), mapping another machine's project roots and
  missing assistants (`remap.go`), and running the installs and the cleanup over the engine.
- `trash`: The trash: one run directory under `$XDG_STATE_HOME/lola/trash` per command, its
  `manifest.txt` journal, copies and moves into it, and `UnlinkIfBackedUp`. Only `errs` and
  `untrusted` besides the standard library (depguard).
- `cli`: Commands, help, aliases, completion, output and prompts; the install flow and its
  `Prompter`, with the huh adapter. Long help (`renderHelp`) prints the global flags once, in its
  preamble, and drops each command's `Global Flags:` block (`globalFlagsBlock`, the text the
  template's `cobraGlobalFlags` renders; `installHelp` panics if cobra's template loses it).
  `lola --version` is a root-only flag whose run is `printVersion`, `lola version`'s.
- `errs`: Errors that carry coaching hints.
- `testenv`: Test isolation (`Isolate()`: fresh `HOME`, XDG dirs, git config, no `LOLA_HOME` or
  `COLUMNS`, and a `PATH` without the developer's assistants) and `ServeGit()`, a loopback git
  server (`ServeGitWith` also shows each request to a spec).
