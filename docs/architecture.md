# Architecture

For contributors. The design spec is `docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`;
this page describes what is built (Plans 1 to 4; 5a, sources, designed in
`docs/superpowers/specs/2026-10-09-plan-5a-sources-design.md`; 5b, module formats and runtime
hooks, designed in `docs/superpowers/specs/2026-10-09-plan-5b-formats-design.md`; and 5c, the
interactive install flow, designed in
`docs/superpowers/specs/2026-10-10-plan-5c-install-flow-design.md`; and `lola migrate`, designed
in `docs/superpowers/specs/2026-10-12-migrate-from-python-lola-design.md`).

## Pipeline

```
source ──fetch──▶ staging ──Put (detect, normalize, digest)──▶ cache (versions, heads per ref)
                                      │ module.Load (IR, re-derived each time; no format adapter)
                                      ▼
                     resolver (cached module minus selections.yaml exclusions)
                                      ▼
                     templates rendered and rechecked, per target (internal/templating)
                                      ▼
                     targets (pure: IR → intents + findings)
                                      ▼
            runtime hook consent (drops unconsented hook groups)
                                      ▼
            plan.Compute (diff vs ledger + disk, conflict matrix) ──▶ Plan
                                      ▼
            hook consent ─▶ module copies swapped in ─▶ pre-install hooks
                                      ▼
            plan.Apply (stage, back up, rename, write ledger, rollback on error)
                                      ▼
            manifest saved (desired state) ─▶ old copies dropped ─▶ post-install hooks
```

`internal/engine` runs this pipeline for one scope; `internal/cli` is thin cobra wiring over it.

## Packages

- `paths`: XDG resolution from an injected `getenv`; no globals. `ProjectKey`, the canonical form of
  a project directory that the project lock and `selections.yaml` share.
- `config`: `config.yaml`, `selections.yaml` and `keep.yaml` (`lola tidy`'s keep-list: a `user`
  list and per-project lists of selectors, project keys canonical as in `selections.yaml`):
  strict loading of untrusted, hand-edited YAML, selectors, effective exclusions, and locked
  node-preserving writes.
- `yamldoc`: Reading a hand-edited YAML file as one untrusted document (non-blocking open, regular
  files only, size cap, one document, no anchors, aliases or merge keys) and the node helpers
  `config` and `manifest` edit with. `ReadNoFollow` reads a file someone else committed: never
  through a symbolic link (`O_NOFOLLOW` where the system has it, and on every system the file
  opened must be the one `os.Lstat` finds). `Decode` reads bytes the caller has already capped
  (a marketplace catalog) with the same guards, plus a refusal of any mapping key given twice,
  which yaml.v3 refuses only when it decodes into a Go value; it applies no version rule.
- `fsx`: Atomic writes, reading a file lola wrote without following a link (`ReadOwn`), symlink-aware
  destination resolution, comment-preserving JSONC edits (object
  members, and array elements of any JSON value), mode normalization. `JSONDeleteMember` removes
  one member with its own comments (the lines directly above it, up to a blank line or the
  previous member) and nothing else: no parent is pruned, so an emptied server map stays (Plan 6d
  §4.5); `JSONDelete` prunes empty parents for lola's own members.
- `module`: The IR, frontmatter parsing, name rules, format detection and the Claude plugin and
  Agent Plugins adapters, loading `lola.yml` (with `components`) + the native layout, hardened
  module JSON reading, rules, hook validation, and normalization.
- `templating`: The Liquid sandbox (Plan 7): `Render` and `RenderDoc` screen, compile and render one
  template and the partials it includes under caps, with lola's 41 filters; `Uses` tells which
  bindings a module's templates read, walking each partial at most once per module, and `AttrRefs`
  lists the literal `attrs` paths they read. It alone imports liquid (vendored in
  `third_party/liquid`) and reads no file: partials come in as `Partials`; depguard enforces both.
- `untrusted`: Makes text lola did not write safe to print: control characters, Unicode format
  characters and line separators replaced, length capped, OS errors without their path.
- `source`: Source parsing and validation (also of sources recorded in `project.yml` and
  `lock.json`, `Recorded`), the folder, git and archive fetchers with their executables, the
  reachability check of a pinned commit, and `GitCommand`, the one hardened way lola runs git (also
  for the engine's git-ignore check and migrate's git-tracked guard, with `InWorkTree`).
  `CheckRemote` is the address rule for sources a stranger's file names (a remote profile and its
  entries; it lives here so a package that checks such sources need not import `profile`):
  `https://`, `ssh://` or git's scp form, at a host given by name, never a path or
  `file://` URL, a URL that does not parse, plain `http://`, `localhost` or `*.localhost`
  (trailing dots included), a loopback, link-local, unspecified, private, carrier-grade NAT or
  site-local (`fec0::/10`) IP address, NAT64's local-use `64:ff9b:1::/48`, an IPv6 address
  embedding such an IPv4 one (`::a.b.c.d`, `64:ff9b::/96`), or an IP address in any but its
  canonical form (a host whose last label is a number, WHATWG's "ends in a number", is an IP
  address to git and ssh; rulings R-6b3b-6, R-6b3b-9). It reads the text alone, never the disk.
  A `Spec` a marketplace's catalog named (`FromCatalog`, Plan 6c §10, R-6c-13) keeps the rule past
  the first request: every redirect of its archive download must pass `CheckRemote`, and git runs
  with `-c http.followRedirects=false` (`remoteArgs`) for its `ls-remote`, clone and fetches, the
  reachability check's included; a redirect git refuses so is reported as `<url> redirects, and
  lola follows no redirect of a source a catalog names` (`refusedRedirect`). `Spec.SHA256`, a catalog archive's listed sum, is checked against
  the downloaded bytes before anything is extracted (`SHA256MismatchError`, R-6c-6).
- `store`: The cache's storage: content-addressed versions with their executables, per-ref heads
  with their commits, `Prepare` (check and digest in the run's staging) and `Commit` (publish under
  the cache lock; `Put` is both), snapshots of a pinned version (`Pinned`), the cache lock.
  (Internal name; user-facing text says "cache".)
- `ledger`, `manifest`: Observed and desired state, validated at load; the ledger written
  deterministically, the manifest edited in place (`manifest.Edit`) with its pins.
- `profile`: Profiles (Plan 6b §4): `Visible` finds the user's profiles and, at user scope,
  `default.yml` by their names alone (at most 256 entries in `profiles/`), and counts the `.yml`
  files whose names fail the module-name rules, which listings report as a count and never by name
  (§19 S7); `Load` reads one through `yamldoc.Read` (following a symbolic link, as for `user.yml`,
  §19 P7), version first, known keys only, at most 512 entries each checked as a manifest entry
  (`manifest.Entry.Validate`), every refusal with the file and line; entries decode into a
  `manifest.Entry` without `market` (`entry`), so a profile carrying a marketplace's provenance is
  refused as any unknown key is (Plan 6c §6.4, R-6c-1); `Entries` gives entries without
  targets the profile's `targets`. `Project` lists a project's committed profiles in
  `<root>/.lola/profiles/` the same way, after refusing a `.lola/` or `.lola/profiles/` that is a
  symbolic link, not a directory, or resolves outside the root (`fsx.Resolve`, §19 S3); a
  `default.yml` there is no profile, and its path is returned for listings to warn about (a project
  cannot define `default`). `LoadProject` reads one as `Load` does but refuses a symbolic link and
  opens it with `yamldoc.ReadNoFollow`, coaching "restore it from version control". `Remote` lists
  the remote profiles in `$XDG_DATA_HOME/lola/profiles/`, one directory each (a name starting with
  `.` is a directory lola is writing, never a profile); `LoadRemote` reads `<name>/profile.yml` and
  `source.json` (`Origin`: version, source, ref, path, commit, digest) without following a link,
  checks the origin (`Origin.Check`: the recorded-source rules, a remote source, a full pin) and
  that the file's bytes have the digest it records, then applies a remote profile's rules: every
  entry names a remote source and carries its pin (§19 S1, D4), a remote source being one
  `source.CheckRemote` takes.
  `Fetched` and `FetchedNames` read `<name>.yml` from the `profiles/` directory of a source the
  engine fetched into staging, refusing a `profiles/` or a file that is a symbolic link before
  reading (§19 S3), with the same rules. No network, no writes; depguard holds its imports (`source`
  for the pin and address rules only).
- `market`: Marketplaces (Plan 6c §4–§6.1), pure: no file system and no network, which depguard
  enforces by denying it `os` and `io/fs`, and forbidigo by denying it the functions of `source` and
  `yamldoc` that reach the disk, the network or git (`source.Parse`, `Fetch`, `yamldoc.Read`, …), so
  no catalog string can reach the disk from it. `Parse` reads a catalog in Claude Code's
  `marketplace.json` format (decoded token by token, so every object keeps its keys in order: a key
  given twice, or a case variant of one lola reads or skips on, is ambiguous, refusing the catalog
  at its top level and skipping the entry inside one; nesting at most 64) or the Python lola's YAML
  (`yamldoc.Decode`) into one model, `Catalog`, after refusing more than 8 MiB, more than 4096
  entries, a catalog name, owner or version over 256 bytes or description over 1024, and any key or
  read string that `untrusted.IsPlain` refuses, naming the field. What decoding allocates is bounded
  too, as the byte cap alone does not bound it: at most 262144 JSON values, and a YAML catalog at
  most 512 KiB and 131072 nodes. Each entry is adapted on its own, and one lola cannot use is a
  `Skip` with its reason: a source kind lola does not take (npm, command, any other), component
  fields other than the content lists (and those on any entry but a relative one), headers and
  headersHelper, a malformed or disagreeing `sha` or `sha256`, an oversized
  field (a version over 256 bytes or a description over 4096 among them), a name that fails the
  module-name rule or that two entries share. Every network source a catalog names is classified
  from its text alone (`remoteSource`): never ssh or scp form, only `https://`, then
  `source.CheckRemote`, `source.Recorded.Check` and `source.Validate`; a full-SHA ref becomes the
  commit pin. Relative entries keep the directory they name (`Entry.Relative`, checked by
  `CheckRelative`), which `Bind` maps to the marketplace's own source, ref and commit from its pin
  (`Pin`, `source.json`), under `Pin.Root` (where the fetched tree was in the source,
  `source.Fetched.Root`: an archive's wrapping directory or a `module/`, `""` for the top), so the
  entry's path is a `--path` that reaches it; skipping those of a single-file marketplace and those the fetch did not
  find (`Pin.Absent`). A relative entry's content lists, `skills`, `commands` and `agents`, become
  `Entry.Components`, paths in its directory checked as relative ones are (at most
  `source.MaxComponents`, 1024, which `Components.Check` also enforces on every file that records a
  selection, checking overlap with a set in O(n·depth)); `Selected` names each
  in the fetched tree for the engine to check (a skill a real directory, a command or an agent a
  real file or directory), and `Bind` skips an entry one of whose paths the fetch did not find.
  `ParseAddress` reads `@<market>/<entry>` (or `@<market>`), both parts module names, and
  `CloseNames` gives a missing entry's near names and `CloseMarkets` a missing marketplace's
  (`closeTo`: case-insensitive Levenshtein distance 2, or containing it; at most 10). `Entry.Spec` is where an entry is fetched from.
  `ParseRegistry`, `AddRegistration` and `RemoveRegistration` read and edit `markets.yaml`, keeping
  comments; `ValidateName` is the module-name rule minus the Python verbs (`Verbs`); `Slug` and
  `FreeName` make the names hints offer; `CatalogFiles` is the lookup order of a marketplace's
  catalog.
- `activeprofile`: The qualified names a scope's active profile list stores in `lock.json`
  (`user:go`, `project:go`, `remote:go`; never `default`), at most 64: a leaf with no YAML, so
  `ledger` imports it and `profile` may import `ledger` later without a cycle.
- `trust`: The trust record (`trust.json`): per project key, the view of `project.yml` lola wrote
  and of each project profile, and its hash; strict, permission-checked reads, locked per-project
  saves, coverage, the per-field refresh, and `Check`: the status and the typed field changes of a
  manifest against its record. No dependency on `engine`.
- `consent`: Recorded consent to a module's install hooks (key `<module>`) and runtime hooks (key
  `<module>/runtime-hooks/<settings-file hash>`), each recording a digest.
- `target`: The target contract: `Target`, `PlanContext`, `Finding`, capability tables and `Admit`,
  `Probe`, and helpers renderers share. No implementations.
- `target/claude`, `target/gemini`, `target/opencode`: The `claude-code`, `gemini-cli` and
  `opencode` renderers, each with its capability table and probe. One package per target.
- `target/conformance`: Specs every registered target must pass.
- `targets`: The registry: the one list of every target.
- `plan`: Intents, `Compute` (diff + conflicts) and `Apply` (transaction over files, ledger and
  manifest).
- `engine`: Install, uninstall, render, sync (`desired`, the trust report, containment, removals),
  tidy, status, fetch, update, search, `Info` (lola info: a version's view, `ModuleView`, which cache add
  and cache list print too, the code it runs, and install's dry run pinned to it, with the consent it
  meets); run staging, module copies, install hooks, runtime hook consent,
  the git-ignore and tracked-ledger checks; every manifest write with its trust refresh
  (`saveManifest`) and the refusals of untrusted entries; install scope resolution, the component
  resolver, selection edits and `InstallChoices`.
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
- `testenv`: Test isolation (`Isolate()`: fresh `HOME`, XDG dirs, git config, no `LOLA_HOME` or `COLUMNS`, and a
  `PATH` without the developer's assistants) and `ServeGit()`, a loopback git server (`ServeGitWith`
  also shows each request to a spec).

## Selections and the install flow

**Files.** `internal/config` reads `$XDG_CONFIG_HOME/lola/config.yaml` (`default_scope`, `pager`,
`pre_install`, `post_install`, `cache_unused_for`) and
`selections.yaml` once per command. Both are untrusted: at most 1 MiB, one YAML document, no
anchors, aliases or merge keys, known keys only, `version: 1`, module names through `ValidateName`,
known kinds, valid `path.Match` globs (no control or format characters in a selector name, from the
file or a flag), no `include` in `global`, absolute project keys. Errors name the file, line and key
path (keys through `untrusted.Text`) with a hint. Project keys pass through `paths.ProjectKey`
(absolute, clean, symlinks resolved; on Windows forward slashes and a lower drive letter) and
compare with `paths.KeyID` (case-folded on darwin and Windows), so two keys for one project are
refused and a hand-written spelling still matches. The project lock file is named after the same
key.

**Resolver.** `resolveComponents` (engine) is the one place a module becomes the components a
scope installs: the cached version minus the effective exclusions (global skip ∪ scope skip −
scope include) and without rules when they are off. `Install`, `Render` and `Check` plan its
output, so a component skipped after installation is an orphan `plan.Compute` removes (or a
conflict, if hand-edited); `Status` reports the skipped names. A pattern that names nothing is
a warning notice.

**Destinations.** An install acts on an ordered list of destinations: at most one user scope
and any number of project roots. `Scope.Key()` names one: `user`, or the project's canonical
key (`paths.ProjectKey`, compared through `paths.KeyID`), so every spelling of a root is one
destination. Everything the flow keeps per destination is keyed by it: `ModuleChoice.Scope`,
`Preview.Selected`, the flow's assistants, component picks and dropped-pick counts. Each project
root is its own `projects:` section of `selections.yaml`. Results carry `scope` and `root` on
`scopes`, modules and steps. The scope picker's **Other directory…** adds a project root; the
flags still give at most one project and the user scope.

**Scope.** Every command takes the root's persistent `--scope`, `--project`, `--user` and
`-C/--root` (Plan 6b §3.1); `app.scopeRequest` turns them into an `engine.ScopeRequest`.
`Engine.ResolveScope` (`internal/engine/scope.go`) owns the rules: `--user` refuses `--project`
and `-C`; `--scope` (which `--user` and `--project` must agree with), then `--user` or
`--project`, then `-C`, then, on a terminal, `Pick` (the CLI runs the picker); off one, a
`.lola/` found walking up, then `default_scope`, else a refusal naming every way to give a
scope, naming the command (`ScopeRequest.Command`), with `--scope both` only when
`ScopeRequest.Both`. Without `Both`, two scopes from `--scope` or `default_scope` are refused
there too. The CLI's `app.scopes` runs it for `install`'s siblings that write (`uninstall`,
`render`, `sync`); the picker loop (`scopePicker`, which install's flow shares) asks a
one-scope command (`ScopeStep.One`) to tick exactly one, nothing preselected, so an empty
answer asks again. `sync` takes both scopes and syncs them in order, project roots first,
stopping at the first that fails or is declined; `--json` prints `{"scopes": [...]}`, or carries
the synced scopes in the error document (`syncedScopesError`). `Engine.ResolveReadScope` is the read-only rule (`list`): the flags when given,
`--scope both` included, else the project found walking up and user scope, never asking and never
reading `default_scope`; each `StatusRow` carries its `scope`. `check` and shell completions use
`app.scope`: the flags as one scope, else the found project, never asking. `lola sync`'s
completion asks the engine (`SyncNames`): every module the scope's layers want, installed or not
(a profile's included), and every module installed there; it takes no lock and runs no git, so
the tracked-`lock.json` check is left to the command it completes.

**Install.** `InstallAll` takes, per destination, the scope and its source (`flag`, `config`,
`discovered`, `picked`, from `ResolveScope` or the picker), its targets and its selection
edits, plus global edits, and refuses a destination listed twice and any project rooted at the home
directory (it renders where the user scope does, so two ledgers would claim one set of files;
`Render` and `Uninstall` still accept one an older lola installed). Order: the selections lock
(`$XDG_STATE_HOME/lola/locks/selections.lock`, always before any scope lock), then every scope's
lock in `lockOrder` (project roots by canonical key, then the user scope, so two runs never wait on
each other in a cycle), then each scope's module resolution; the edits applied to `selections.yaml`
in memory (`applyEdits`: `--only`, then `--skip`, then `--unskip`); planning through the resolver;
every refusal and prompt for every scope (`prepare`: runtime hook consent, `--strict`,
`plan.Compute` conflicts, install hook consent); `config.Save`; then `commit` per scope. A refusal
or cancel writes nothing; a failure while applying leaves `selections.yaml` written and says the
next `render` converges. `config.Save` re-reads the file under the lock, replaces only the changed
modules' entries through the yaml.v3 node API (comments and order survive, also in a comments-only
or null document; a section or project key a removal empties is dropped unless it carries a comment,
then it stays as an empty mapping), writes atomically at a symlink's target with mode 0600, and
never overwrites a file that does not load.

**Choices and the flow.** `InstallChoices` computes everything the pickers show: per destination
the assistants and the manifest's fixed targets; per cached module (sorted by source, then name)
its components with first-line descriptions, and per destination whether it is installed, what
is excluded and which components are installed now. `Choices.Preview` applies a run's edits to a
copy of the selections with the same `applyEdits`, so the summary shows exactly what the install
will do; `ModuleChoice.Diff` turns that into `+ ~ - =`. `Choices.Versions` and `ModuleChoice.ID`
record the cached versions listed; `installFlow` keeps them as pins (`InstallRequest.Version`)
through every "Change …" step, and `pinnedVersion` stops the run when a pinned version left the
cache. Two arguments resolving to different versions of one module are refused when the choices
are computed. For a module picked from the cache, `listedVersion` lists, per scope, the head of
the ref that scope's entry records (the default branch's for an entry without a ref); a scope
with no entry takes the listed head and records its ref. When no version can be listed (the
entry's ref has no cached head, a ref-less entry whose default branch is not cached, scopes
resolving to two versions, a git head cached before pins with no commit for `pinCommit`) the
module is left out of `Choices.Modules` and `Choices.Warnings` names the command that installs
it. A kept pick that `listedVersion` no longer returns is refused while the choices are
computed, before the summary, as is a module argument whose git version `pinCommit` could not
pin. The CLI's `installFlow` only fills answers: each step asks the `Prompter` (one method per
step and one for the review menu) unless a flag or the situation answers it, component picks
become `--skip`/`--unskip` edits, and the equivalent commands are built from the same spec.
`Prompter` implementations: the huh adapter (inline forms on stderr; accessible line prompts
under `LOLA_ACCESSIBLE=1` or `TERM=dumb`, with an `eofReader` that turns end of input into a
cancel), the non-interactive one, and the specs' scripted one. In accessible mode every prompt,
hook consent included, reads through the `eofReader`, so end of input is `ErrAborted`. Specs
parse the equivalent commands back through the real command tree and compare the plans.

## Plans and the ledger

Targets return **intents** (`WriteFile`, `CopyTree`, `SetJSON`, `AddJSONMember`).
`plan.Compute` expands trees, then, in two passes:

1. **Desired.** Each intent's destination is read at its symlink-resolved location and
   classified: new → create; identical → adopt (recorded, not rewritten); lola's and unchanged
   → update; lola's but hand-edited, or not lola's and different → conflict; owned by a module
   that is not being replanned, or claimed twice → non-forceable conflict. Two logical
   destinations that resolve to the same real file (symlink aliasing) are refused. A file whose
   bytes match but whose normalized mode differs is a **mode-only update** (noted as
   `mode changed`), not an adoption.
2. **Orphans.** Ledger artifacts nobody claimed are deleted if unchanged, kept as conflicts if
   hand-edited, forgotten if already gone. Running desires first lets a destination move between
   modules in one operation without a spurious conflict or delete.

The result is byte-exact `FileChange`s and the ledger that applying them produces. A dry run is
exactly this computation. Each ledger entry records the targets its module is installed on
(`ledger.Entry.Targets`, `targets` in `lock.json`), since a target can render none of a module's
files and still have run its hooks: `updateLedger` keeps the entry's targets outside the change's,
adds the change's entry targets (`ModuleChange.Entry.Targets`, the engine's `ModuleResult.Targets`)
and every target it writes an artifact on, and drops the entry once no target is left.

A `CopyTree` intent may carry an `Overlay`: files `expand` writes into the tree beside the ones it
walks, mode 0644, under the tree's consent. Targets put rendered template support files and
sidecars there and exclude their sources (`target.SkillTree`, `target.SidecarTree`). An entry of a
module with templates records `rendered_with` (`ledger.RenderedWith`): the active profiles when a
template reads them (`templating.Uses`), and `vars`, the winning entry's overrides as written. An
edited, invalid one (vars read by the entry rules, `module.DecodedVars`) is read as absent, so the
module renders again. `lock.json` and `trust.json` decode with `UseNumber`, a number reading as
`int64` when integral and in range, else a finite `float64`, so a large integer stays exact and a
record still matches its hash; a float is written with a point or an exponent (`module.Vars`), and
read back as one, so `2.0` and `2` stay two values in `SameVars`, `sameRendering` and the trust
view's hash (R-7-50): an edit between them re-renders and asks trust again.
`ledger.Load` refuses targets that are not target names, are listed twice, or omit an artifact's
target. An entry from before the field falls back to its artifacts' targets
(`Entry.InstalledTargets`).

**Profiles in the ledger** (Plan 6b §5). `profiles` is the scope's active list, in order: each
item a qualified name (`user:go`, `project:go`, `remote:go`; never `default`) and the action that
put it there (`by`: `on` or `switch`), at most 64, each listed once. A module's `from` names the
profile whose entry won when lola last applied it (`profile:user:go`, `profile:default`); an
entry the scope's manifest won has none. Both keys are left out when empty, so a ledger without
profiles is written byte for byte as before them, and the version stays 1. `ledger.Load` refuses
a list or a `from` lola never writes.

**Replaceable paths.** `Request.Replace` (from `ScopeInstall.Replace`) lists unowned
destinations the caller lets the plan overwrite instead of refusing: a file, every file below a
directory, or a JSON member at one pointer. Only the "exists and was not written by lola" case
changes: the step is an update marked `Replaced`. Ownership still wins (a module's path, or its
own hand-edited file, stays a conflict), and a path with a symbolic link between the target's
root and it (`fsx.Linked`) is never replaceable. lola migrate passes the Python lola's recorded
paths after its guards, and backs up exactly the steps marked replaced.

Hashes: files are hashed by bytes, JSON members by their canonical JSON (sorted keys, no
comments). JSON edits operate on the hujson AST so bytes outside the edited member are
preserved; trailing line comments belong to the line they end. `ledger.created` remembers the
JSON files lola created, so they are deleted when lola's last member leaves them empty.

**Owned array members.** `AddJSONMember` claims one element, any JSON value, in a JSON array that
users also edit: a string for rule references (Gemini and OpenCode), an object for a runtime hook's
matcher group (Claude Code). Elements compare as canonical JSON, so key order and layout in the
user's file do not matter. The ledger records a string as `value`, as it always has, and any other
element as `member`, in canonical JSON; either is part of the artifact's identity, so several
modules can each own an entry in one array, and the hash is over the element's canonical JSON. Files
and step lines get the element with `& < >` unescaped (`fsx.ReadableJSON`), so a shell operator
reads as one. A ledger `member` that another tool reformatted is recorded again as canonical JSON at
load; one that is a string or `null` is refused, with a hint. A member is present or absent, never
hand-edited: one the user removed is rewritten with a note, and an orphan the user removed (or
edited, which changes its identity) is forgotten. A string the user already listed is adopted; any
other element the user already has is a forceable conflict, since owning the user's own hook group
would delete it on uninstall. A `created` entry with a `key` records an array lola created; when
lola's last member leaves it, the array goes too, with the parent objects it leaves empty, and the
file if lola created that. A value at the pointer that is not an array is a hard error. Appending
keeps comments that follow the last member before the closing bracket or brace, so removing lola's
entry restores the file byte for byte.

**Rules** are plain files each target writes where its assistant loads them, never in a
user-owned instruction file. Claude Code loads `.claude/rules/lola/<module>/<rule>.md` as is.
Gemini CLI has no rules directory: a module's rules are joined into
`.gemini/lola/<module>/GEMINI.md`, and that directory is an owned member of
`context.includeDirectories` in `.gemini/settings.json` (Gemini walks it for `GEMINI.md` in
trusted folders; observed with 0.63.0). OpenCode gets one file per rule in
`.opencode/lola/<module>/` and one owned glob member in `instructions`. At user scope the members
are absolute, because user settings apply in every project; OpenCode expands a wildcard only in
the last component of an absolute entry, hence one member per module. Every artifact belongs to
exactly one module.

## Templates

A Markdown component named `<name>.md.liquid` is a template (Plan 7). `module.Load` finds it
wherever it finds the literal file (native layout, `components` entries, both format adapters),
reads it capped (256 per module, 256 KiB, UTF-8) and never parses it: `Doc.Template` is set and
`Doc.Body` holds the source; support files and sidecars record their source path. Load refuses a
literal file beside its template (names compared case-folded) and warns about case variants. It
reads partials the same way, from regular files in `_partials/` at the module root only
(`loadPartials`, `internal/module/partials.go`: `<name>.liquid` or `<name>.<target>.liquid`, 256
files, 256 KiB each, 4 MiB together), into `Module.Partials`; other entries warn. A root skill
excludes `_partials` and its case variants, so partials never reach a target; only the root
entry named exactly `_partials` is read, so a case-folding file system cannot load `_Partials`,
and a component path under `_partials` is refused.

`planModule` and `Check` render between the resolver and the targets, once per target, into a
copy of the module (`renderTemplates`, `internal/engine/templates.go`), so no target ever sees a
template. For each component template: `templating.RenderDoc` renders, with the module's
partials, the frontmatter region, up to the first literal `---` line at block depth 0, and the
body region as two templates of one file sharing its deadline, output cap and assign budget;
`module.RenderedDoc` parses the frontmatter with `ParseDoc`'s rules and refuses a rendered line
that is `---` or `...` with only spaces, tabs or a carriage return after it
(`module.ErrRenderedFence`); a `name:` other than the path's, or one that is not text, is
refused; then `module.Recheck` reruns, on a module holding only that rendered component, the
load checks that read frontmatter (hooks, an agent's MCP servers, sidecar hooks, an Agent
Plugins skill's agentskills.io rules) and `module.RenderedRule` a rule's `paths:` refusal;
either refusal is a `template-rendered-check` `TemplateError` with `rendered` set. Between the
name check and `Recheck`, the component's attribute file for the target is merged in
(`withAttributes`); a literal component with one gets the merge and `Recheck` alone, its refusal
naming the attribute file "(merged for <target>)" (JSON keeps `rendered`). When the file adds
keys to a template's output, `Recheck` runs on the template's output first, so a refusal after
the merge names the attribute file too. Targets then compute intents, findings and consent on
the rendered copy, so runtime-hook consent, keyed by a digest of the hooks as rendered, asks
again whenever a template renders them differently. No value can break a line in frontmatter
(below), so a value adds a key only where the template puts it at a line start; there is no key
guard (Plan 7.5, R-7-U11): what such a key can execute, hooks and MCP servers, is gated by consent
under every spelling each target accepts, or dropped with `frontmatter-key-dropped` where the
assistant does not read it (a conformance spec walks the registry), and any other key comes from
the author's defaults and attribute files or the user's vars, which trust covers. Residual: a
capability key (`permissionMode`, `allowed-tools`, `tools`, OpenCode's `permission`) is
reachable that way, bounded by trust, plus a warning on Claude Code.

**Frontmatter values.** Before compiling a region or a partial, `withValueCheck` appends
` | lola_frontmatter_value` to the markup of every `{{ }}` outside `raw` and `comment` that is
its line's whole value or whole key and whose last filter is not `json` (`wholeValue`, from the
source text around the token), and ` | lola_frontmatter_break` to every other, keeping the token's
delimiters, trims and whitespace (an object may span lines through the whitespace around its
markup, so rebuilding it would move lines); a lone `-` (`{{-}}`) stays markup. The filters'
closure knows the region: in the frontmatter both refuse a value when any string liquid would
write for it, through lists and maps and map keys included, holds a YAML line break (yaml.v3's
`is_break`: `\n`, `\r`, U+0085, U+2028, U+2029); in the body they return their input. `json`
escapes all five, so a json-ended value passes, and a filter after `json` is checked on its own
output. `lola_frontmatter_value` also refuses a string that YAML would not read back as itself
written as a plain block-context scalar (R-7-U18): the engine passes `module.PlainRead`, which
parses `k: <value>` with yaml.v3, to `RenderDoc`, so the depguard-fenced templating package
parses no YAML. Only a whole value or key is checked (R-7-57): a value composed with the
author's text (`v{{ x }}`, `"{{ x }}"`) is the author's to shape, so a ` #` there can still cut
it short. Numbers and booleans are not checked. The check is a data-correctness aid, not a
security boundary: consent and the recheck see the parsed result (R-7-58). It also misses a
`{{ }}` inside a tag on the same line (`{% if c %}d: {{ v }}{% endif %}`), one followed by a
comment (`# note`, a `{% comment %}` tag), one under a quoted or `?` key, a value a partial
included on the line writes, and a list or nil value (a list prints concatenated, nil empty);
the advice is `| json`. Markup
the appended filter cannot keep valid stays invalid and refuses at compile. The refusal,
`template-frontmatter-value`, is mapped from the filter's error at the object's line: for a line
break quoting the markup when it is the line's only `{{ }}`, for a round trip naming the value
and what YAML would read.

A template refusal is an `engine.TemplateError` (module, target, scope, component, file, line,
code, reason, and for a refusal in a partial the partial's file and `IncludedFrom`, its first
include site): it refuses the run before anything is written, with a `lola check` hint where one
reaches the version refused (`checkHint`: never for a pin but a fresh install's,
`Options.freshPin`, since check fetches the newest version); `lola check` turns each into a
`skip` finding and keeps going, except a timeout or the run budget. A plan pass (`templateRun`)
holds the active profiles every template sees and a budget of 20 s of rendering and 32 MiB
rendered, charged for every render, failed or not. sync's trust diff and its first pass share
one; the pass after a partial trust answer starts a fresh one; every profile one `lola profile
--add` or `--update` diffs renders under one. The diff renders the version installed with the
profiles and vars `rendered_with` recorded, so a changed list or vars edit shows in its hooks;
without a valid record it renders the defaults, never the new entry's vars. Residual: sync
publishes a covered pinned entry to the cache before it plans (Plan 6a), so a template refusal
leaves that cache head moved; nothing else changes and the run directory is removed. A profile
switch re-plans, besides the modules it moves, every installed module whose recorded
`rendered_with` differs from what the new list renders with; `templating.Uses` follows includes
into the partials of every target, so a module that reads `profiles` only in a partial counts.

**The sandbox.** `internal/templating` builds a fresh `render.Config` per render with the standard
tags and lola's own filters, never liquid's, and strict variables. A screen over liquid's token
scanner refuses before compiling what liquid would accept but lola does not: denied and unknown tags
(`capture`, `tablerow`, `render`), an `include` of anything but a quoted partial name, `..` outside
string literals in markup (ranges are Go structs whose methods liquid calls; strings are stripped
exactly as liquid's expression scanner reads them, `scanner.rl`, and an unterminated quote leaves
the rest outside, so a lexer difference fails closed; a spec compares the two on a corpus in every
position a range can take), more than 256 `assign`s, blocks or parentheses nested over 64,
undeclared `vars` names, `vars` or `attrs` printed whole, `forloop` used other than through a
property or a quoted literal key, and rebinding `forloop`, `vars` or `attrs`. Bindings are plain
maps (as sorted `IterationKeyedMap`s), lists, strings, booleans and numbers (`int64`, finite
`float64`), so no Go method is reachable. `attrs` is bound as a `values.Value` (`lenientValue`):
liquid's `ValueOf` returns a `Value` as it is, so templates reach it only through its methods, and a
key or index it lacks, at any depth of a path, reads as an `absent` value (nil, false, equal only to
nil) instead of liquid's `undefined`, which strict variables refuse; `Interface` hands `for`,
filters and `json` the plain sorted maps, so an alias reads strictly again. `vars` stays strict, its
names checked before rendering. `lola check` warns (`attrs-undefined`, `engine.attrsUndefined`) on
each literal `attrs` path no attribute file of the module defines for any target; a property of a
scalar under `attrs` reads as absent too, but `size`, `first` and `last` keep liquid's meaning. The
math filters (`plus` … `abs`) follow liquid's numeric semantics on `int64` and `float64` and refuse
an `int64` overflow, a non-finite result and a zero divisor, where liquid would wrap, print NaN or
coerce a string; `divided_by` divides two integers in `int64`, exactly, where liquid goes through a
float. Every growing filter checks its result against 64 KiB and 4096 items before allocating;
output is capped at 1 MiB per file and each render runs under its own deadline in a recovering
goroutine. Memory is so bounded by construction: values arise only from capped literals, bindings
and filter results and outlive their expression only through `assign`. A loop that neither writes
nor filters cannot be interrupted; Render returns at the deadline and the refused run ends the
process. Residual risks: an engine-internal growth path not found in the review; a non-yielding
loop's CPU until exit; a map nested in a list prints in Go syntax.

**Partials.** Because include names are literals, `screen` (`internal/templating/graph.go`) builds
each template's include graph for the target before compiling: it resolves each name to
`_partials/<name>.<target>.liquid`, else `<name>.liquid`, screens every partial reached once, and
refuses a cycle, a chain of more than 8 partials (`Limits.Chain`), and more than 256 `assign`s or 64
open blocks across the template and its partials. A name with no partial for the target is a missing
leaf, with no file and no edges, so it counts in none of these; the store refuses it
(`template-include-missing`, at the include tag) only when a rendering include asks for it, so an
include under a false `{% if %}` needs no partial. A partial the frontmatter includes gets the
frontmatter's value check, and a `---` or `...` line it writes is left to the rendered check. The
render's `TemplateStore` (`store`) serves only the graph's partials, under exactly the paths liquid
asks for: the template's directory joined with the name (liquid compiles a partial under that path,
so a nested include asks in the same directory). Every read counts and the 65th refuses
(`Limits.Reads`); no error wraps `fs.ErrNotExist`, so liquid's own cache is never consulted. An
error liquid reports under such a path is mapped back to the partial's file and line, with its first
include site in graph (depth-first) order as `IncludedFrom`: liquid does not report which site was
rendering. A partial inherits the includer's bindings; its own `assign`s stay in its scope.

**The vendored liquid.** `osteele/liquid` v1.9.2 is vendored in `third_party/liquid` with one
patch (`PATCHES.md`: chained right-trims flush liquid's trim buffer), under a `replace`.
`govulncheck` therefore reports the module as `./third_party/liquid` with no version and cannot
match advisories for `github.com/osteele/liquid` to it: those advisories are checked by hand
whenever the copy is touched and before each lola release ([release.md](release.md), item 3).

## Targets

The design is `docs/superpowers/specs/2026-10-09-target-framework-design.md`.

- **One package per target** (`internal/target/claude`, …) implements the contract in
  `internal/target`; `internal/targets` lists them explicitly (no `init()` registration). A
  `depguard` rule in `.golangci.yml` lets target packages import only the contract (exactly, not
  a sibling target), `module`, `paths`, `plan`, the standard library and their vetted
  third-party packages (`yaml`, `toml`, `semver`), never the engine, the CLI or lola's state.
- **Scope and root.** `Detect(scope, root)` gets the target's root for the scope, and
  `PlanContext.Root` carries it into `Plan`. OpenCode needs both: its user-scope root is its own
  config directory, whose existence marks it as set up, and it writes to `opencode.jsonc` when
  the root has one, else `opencode.json`. Targets may read the disk this way; they never write.
- **Destinations.** `Destinations(scope, root)` declares, pure like `Plan`, where the target
  writes: directories whose direct entries are units of one kind (`DestDir`: kind, path, and the
  extension stripped from a file's name) and the JSON objects of MCP servers (`ServerMap`: file
  and pointer). `lola tidy` sweeps only these (Plan 6d §4.1). Rules are declared in lola's own
  namespace directory (`.claude/rules/lola`, `.gemini/lola`, `.opencode/lola`), never the user's
  `.claude/rules`; `.gemini` and `.opencode` themselves, which hold the assistants' settings, are
  never declared. A conformance spec ties them to `Plan` both ways: every file and MCP intent
  lies in a declared destination of its kind (a direct entry, or for skills and rules one level
  further down), and every declared destination receives one; `OpAddJSONMember` intents are
  exempt.
- **Findings, not errors.** A target returns what it loses as `target.Finding`s with a severity:
  info, warn, or skip (that artifact is left out for that target). `Plan`'s error is only for
  failures that are not about module content. `target.Skipped` applies `--force` (forceable
  skips are rendered, marked `Forced`); `engine.StrictError` decides what `--strict` refuses.
  Load warnings become findings about the module; the engine's own messages stay `Notice`s.
  A finding the user can act on carries a `Hint`. `Admit` names the attribute-file entry, name
  pattern or version; each target names its own template branch or change; and the engine
  appends the run with `--force` (`Options.ForceRun`) to a forceable skip. The conformance
  suite's coaching spec drives each target's live table and own findings, and fails on a
  hintless finding its `nothingToDo` list does not explain.
- **Capability tables.** Each target declares `Capabilities`: supported artifact kinds,
  frontmatter keys with value shapes and patterns (or always dropped), required keys, name
  patterns, and content rules. `target.Admit` checks one artifact against them, returns the
  document without the dropped keys, and warns about YAML values that 1.1 and 1.2 parsers read
  differently (`module.Doc.Ambiguous`). Renderers render through `Admit`, so the table is both
  what lola checks and what it does. Value-dependent checks stay in the target's code and
  return the same findings.
- **Probes.** `Detect` combines a binary lookup over the absolute `PATH` entries
  (`paths.Paths.Search`; relative entries are ignored, so a repository cannot plant a binary)
  with the target's project markers, and executes nothing. Install, render and check run each
  participating assistant once with its version arguments (`Engine.ProbeTimeout`, no shell),
  reading at most 4 KiB of its output, cached per binary build in
  `$XDG_CACHE_HOME/lola/probes.json`. The version only feeds
  `PlanContext.Version`, which `Admit` compares with capability `Bounds` to add findings;
  rendering always follows the newest version, so output is the same on every machine.
- **`lola check`** (`engine.Check`) stages a source like `cache add` into a temporary directory
  and detects its format (or reads a cached module), runs every target's `Plan` and keeps only
  the findings.

## Module formats

Detection (`module.Detect`) runs only on fetched content: `Store.Put` (cache add, install of a
source, update) and `engine.Check` of a source. It walks ADR #243's precedence: a lola
manifest, else the first plugin manifest an adapter accepts (root `plugin.json` with the Agent
Plugins `$schema`, then `.claude-plugin/plugin.json`), else the native layout. Every
lower-precedence manifest present is recorded in `Module.Shadowed` with a warning; an adapter
that finds the file is not its format declines with a warning and detection moves on.

- **Adapters run once and are written down.** An adapter (`module.adapter`: a manifest path
  and a `read` that returns a `lolaFile`) converts the plugin manifest into the same structure
  a `lola.yml` decodes into: metadata, `format`, a `components` section that lists every kind
  explicitly (an empty list turns discovery off) and `formats.<format>` for data lola does not
  use. That structure is applied and checked by exactly the code that reads a `lola.yml`, and
  `Normalize` writes it as the cached `lola.yml`, next to the plugin's untouched files.
- **The load path is format-free.** `module.Load` is detection with no adapters, and it is how
  the cache's versions are read: the generated `lola.yml` wins precedence, and its
  `components` make discovery irrelevant, so the plugin manifests left in the cache are inert
  and a lola upgrade cannot reinterpret a cached version. A spec loads each format's cached
  copy this way and checks that every target renders it exactly like the fetched one, and that
  normalizing it again changes nothing. A cached load does not see what the adapter warned
  about, so `Store.Put` returns the version with detection's warnings.
- **`components`** (`internal/module/components.go`): per kind, path or inline entries with
  optional per-entry `targets`. Paths are checked element by element with `Lstat` (local,
  existing, no symlink); names follow `ValidateName`; two artifacts of a kind and name whose
  effective targets overlap are refused. Targets honor per-entry `targets` for every kind,
  rules included. Runtime hooks (`Module.RuntimeHooks`, canonical JSON of the event map) are
  checked down to the handler fields lola rewrites or shows; only `claude-code` lists the
  `runtime-hook` kind (see [Runtime hooks](#runtime-hooks)).
- **Module JSON** (`mcps.json`, `plugin.json`, `.mcp.json`, `mcp.json`, hooks files) is read by
  one helper: a regular file, not a symlink, at most 1 MiB, nesting at most 64 levels, and no
  object key repeated at any depth (a token walk before decoding, naming the key's JSON
  pointer).
- **Defense in depth.** The planner refuses one module claiming a destination twice ("provided
  twice by module X") rather than letting the later intent win.

## Module copies

`${PLUGIN_ROOT}` expands to the module copy, `.lola/modules/<module>/` (user scope:
`$XDG_DATA_HOME/lola/user-modules/<module>/`), never into the cache. The path does not change
between versions, so rendered files stay valid while a copy is replaced. Targets read artifact
content from the cache version (`PlanContext.ContentDir`) and expand `${PLUGIN_ROOT}` to the
copy (`PlanContext.PluginRoot`), because the copy is only put in place after the plan is
computed.

At project scope the engine refuses to run when `.lola/`, `.lola/modules/` or
`.lola/plugin-data/` is a symlink (`refuseSymlinkedState`, before the lock is taken, also for
dry runs and `status`): a repository can commit one, and the copy swap would then rename and
delete directories outside the project, or plugin data directories would be created there. The
same check refuses a symlinked `.lola/project.yml`, which the manifest write would otherwise
replace outside the project. User-scope copies and `user.yml` may live behind a symlink;
`manifest.Edit` then writes at the link's target.

Install hooks build inside copies (`npm install`, virtual environments), so a copy's content is
never compared with the cache. `render` and `sync` replace a copy only when its module's version
changes (the ledger digest differs, or there is no ledger entry), the copy is missing
(`needsCopy`), or the module gains a target the ledger does not record it on (`addsTargets`), whose
hooks never ran; an entry that is not a real directory, such as a committed symlink, counts as
missing. `install` plans every copy as replaced from the hashed version, so its hooks run only
in bytes the consent was given for; a copy that holds that version already, for an install
that adds no target to those the ledger records, is planned forced (below) and kept when no hook
runs. A target the module is not installed on has never had its hooks run, so an install, sync
or render that adds one replaces the copy and declines, or refuses, hooks without consent. Each replaced copy is
assembled as `.<module>.new`, the current one moves to `.<module>.old`, and the new one takes its
place. If the ledger is never committed (the `after` callback of `plan.Apply` runs only after the
commit), the old copies are restored; otherwise they are dropped. Copies of modules that leave the
ledger are removed. `status` reports a missing copy as `missing`, and `render` restores it and
reruns its hooks; edits inside a copy are not reported.

## Install hooks

`pre-install` and `post-install` scripts must be executable regular files inside the module
(checked when the module is loaded). After the plan is computed and before anything is written,
the engine decides consent per module: a recorded consent for the same digest (sha256 over each
hook's event, script path and script sha256), a `--trust=hooks` (its grant recorded
only after the receipt, [Trust kinds](#trust-kinds)), or the CLI's prompt, whose yes is recorded
in `$XDG_STATE_HOME/lola/consent.json` with the run's grants, after its receipt is answered yes and
before the first write. Without consent a
`pre-install` hook is an error and a `post-install` hook is declined with a warning.

Hooks run once per target, in the module copy, with the parent environment plus `LOLA_MODULE_NAME`,
`LOLA_MODULE_PATH`, `LOLA_PROJECT_PATH` (the target's scope root), `LOLA_ASSISTANT`, `LOLA_SCOPE`
and `LOLA_HOOK`, under a 10-minute timeout. A failing `pre-install` hook restores the old copies and
writes nothing; `post-install` hooks run after the commit, so a failure is a warning. `install` runs
hooks for every module it installs that has consent (a module whose copy it keeps, not pending,
asks nothing off a terminal and is not refused for a `pre-install` hook); `render` only for modules
whose copy it replaces (a new version, or a missing copy). A dry run reports hooks as `would-run`
(`would run` in human output).

A `post-install` hook declined for want of consent is **pending**: `prepare` sets
`ledger.Entry.HooksPending` for a module whose post-install hook it declined, clears it for one
whose hooks run, and keeps it for a module whose hooks the run does not reach. Consent is keyed
on the scripts of the hashed version (`hookScripts(c.src, …)`) while hooks run in the module copy,
which `needsCopy` never compares, so a hook must never run in the kept copy: `Sync` plans a
pending module's copy as replaced (`moduleCopy.forced`), swapped in again from the snapshot or
staged tree, and `Install` plans every copy that needs no new version and adds no target the
same way; their hooks
go through `planHooks`. When no hook job is planned for a forced copy, `keepUnhooked` undoes the
replace before `commit`, so the copy, what earlier hooks built in it and its mark stay (a sync is
still in sync). A forced copy whose hook is not pending (`moduleCopy.pending`) is skipped
without a notice or a `pre-install` refusal when consent is missing. A planted
`hooks_pending` in an untracked `lock.json` therefore runs nothing the user did not consent to
for those bytes. The declined-hook notice names `lola sync <m>`, and off a terminal
`lola sync <m> --trust=hooks`, with the scope's flags. A pending hook is offered only to a run
that names its module or gives `--trust=hooks` (`planSync`'s `named`): any other run plans the
copy as it is, asks nothing, and adds the notice `pending post-install hook for <m> → lola sync
<m>`, so an unrelated full sync never stops at the hook question.

### Install scripts

`internal/engine/scripts.go` adds the user's (`config.Config.PreInstall`/`PostInstall`, absolute
or `~/` expanded at load), the project's (`manifest.Manifest.PreInstall`/`PostInstall`, which
`manifest.LoadUser` refuses in `user.yml`) and the run's (`Options.PreInstall`/`PostInstall`,
made absolute by the CLI against its working directory) scripts around every module copy a run
installs or replaces. `planHooks` returns a `hookJob` for each such copy, its `scripts` the hooks
that run in it (none when it has none or they were declined), so a module without hooks is
wrapped too; a forced copy whose hooks do not run gets no job, `keepUnhooked` keeps it, and
nothing wraps it. `prepare` checks the scripts (`installScripts`) before anything is asked, when
any copy may be written, then `planScripts` decides each project script's trust once (a recorded
consent under `consent.ScriptKey(<resolved project dir>, <event>)` for `sha256:<script sha256>`,
`--trust=scripts` through the receipt, a batch's `Expect`, or `Options.AskScript`), and `commit`
runs them through `runHooks`: per job, the pre-install scripts in order user, project, flag
before its hooks, and the post-install ones in order flag, project, user after them, once per
target.

Each script is held as an absolute, resolved path from planning on: the project's refused when its
path is not plain text (a newline would forge prompt lines and split `--plain` rows), is absolute
(it would name one machine's checkout) or has a `..` component (`filepath.Join` cleans `link/..` lexically, so lola would run another file than a shell
reading the path finds), then resolved under the canonical project root, contained in it
(`source.IsWithin`), not itself a link, and shown from then on as its path from that root beside
the root (S1, A27); every script path is shown on one line (`oneLine`). The
user's and the flag's resolved with `EvalSymlinks`. `runHook` executes that path (never
`filepath.Join(j.dir, …)`, so a file of the same relative name in the module copy never runs),
with `Cmd.Dir` the module copy and `LOLA_SCRIPT_SOURCE` added; a module hook's environment drops
any `LOLA_SCRIPT_SOURCE` inherited from lola's own. The digest taken at planning is
checked again (`Lstat` regular file, sha256 equal) just before each exec; a mismatch is a failure
of that script (pre: the run stops and the copies roll back; post: a warning). The window between
that check and `exec` remains, the class module hooks have.

Project scripts are planned only when the scope is that project (A9). A `Sync` also plans them
when it writes no copy (`Options.askScripts`, R-9-33), reading only the project's (`runOrder` of
`projectScripts`): an untrusted pre-install script is asked about for later runs
(`ScriptQuestion.Later`) or refused off a terminal, and its dry run's command adds
`--trust=scripts` (`Result.needs`); a post-install one is not asked about, only warned of. A
declined post-install script stays in the run's list (`installScript.declined`) so `runScripts`
records it in run order. A mark whose script project.yml no longer names is cleared. A project
post-install script declined for want of trust sets `ledger.Entry.ScriptsPending` on every copy
it would have wrapped; `planSync` offers a pending one (`moduleCopy.pendingScript`) only to a run
that names the module or gives `--trust=scripts`, and `planScripts` runs it alone, per target, in
the existing copy (it is not replaced: consent covers the script, not the module's bytes) only
with consent for the script's current digest; otherwise the copy keeps its mark and the notice
`pending post-install script for <m> → lola sync <m>`. `prepare` keeps the mark of a copy whose
script runs alone, and `commit` clears it, saving `lock.json` again, only once the script ran on
every target, so a failed one is offered again. A dry run plans a pending script as the run does:
counted as a change (`scriptPlan.alone`) when trusted, else listed as needing trust with the notice.

A refusal for want of trust hints every kind the run would still be refused for: `prepare` decides
them before anything asks (`unaskedKinds`: a pre-install hook, and the project's pre-install
script, without a recorded consent, when the run cannot ask about that kind), and the hook
refusal's and the declined runtime hooks' hints add them, so the run a hint names is not refused for
the next kind (N1).

## Runtime hooks

`target/claude` renders each matcher group of a module's runtime hooks as one `AddJSONMember` intent
(kind `target.KindRuntimeHook`) under `/hooks/<Event>` of `.claude/settings.local.json`, or
`~/.claude/settings.json` at user scope. Command handlers get `CLAUDE_PLUGIN_ROOT` and
`CLAUDE_PLUGIN_DATA` (`PlanContext.PluginRoot` and `PluginData`, the same values `target.Expand`
uses): textual forms are replaced in one pass, shell form gains an `export` prefix, and exec form
becomes `env -- NAME=value NAME=value <command> <args>`. `--` must come before the assignments: GNU
`env` reads a `--` after them as the command. Paths that are not shell-safe, commands `env` would
misread, PowerShell handlers and a settings file git could commit are forceable skips of the whole
set; forced renders export single-quoted values and refer to the variables, so a POSIX shell never
parses a lola path as shell syntax (an unquoted reference is still split at spaces and
glob-expanded, so the docs tell authors to quote it). A path holding `' " $` `` ` `` or `\` is never
forced, since the forced form relies on those characters' quoting rules. An event that is not an
identifier is an unforceable skip. On Windows only exec form fits: every `C:\` path holds `\`, so
shell-form hooks are never forced there, and the skip's hint names exec form alone.

**Git-ignore check.** Targets stay pure, so the engine computes
`PlanContext.LocalSettingsExposed` once per command (`probeRun.localSettings`), at project scope
and only for modules with runtime hooks: if the project or a parent holds `.git`, it runs
`git ls-files --error-unmatch` (status 1 means untracked) and `git check-ignore -q --no-index`
(status 0 means ignored) through `source.GitCommand`, whose `-c` overrides keep a cloned
repository's config from running programs (fsmonitor, hooks). Any other status, or no git, sets
the reason, and the target skips the hooks (fail closed).

**Frontmatter hooks.** Claude Code also runs a `hooks:` key in skill, command and agent frontmatter.
`module` checks every key equal to `hooks` ignoring case like `runtime_hooks`; `target/claude`
(`componentHooks`) gives their command handlers the same rewrite and renders the rest of the
document through `target.Expand` separately, so `Expand`'s raw substitution never reaches hook shell
text. Two load refusals close the ways around this: hooks in a `.md` file under a command's sidecar
directory (Claude Code reads it as a command; lola copies it as a file), and a first line starting
with `---` that `ParseDoc` does not read as frontmatter (more dashes, trailing whitespace or text,
no line break), where another parser might find hooks. `allowed-tools` is installed as written, with
the warning `claude-code/allowed-tools`.

**Consent.** Invariant: every intent of kind `target.KindRuntimeHook`, every intent of a component
carrying hooks, and every intent of an agent with inline MCP servers (Claude Code starts them with
the agent) passes the consent gate. Such intents carry a `plan.HookConsent` naming the settings file
whose consent covers them (and, for a component, its kind, name, hooks and, for an agent, its inline
servers as rendered); an intent without one is refused if it has the kind `KindRuntimeHook` or,
whatever its kind, edits `/hooks` in a `.claude/settings.json` or `settings.local.json`. In
`execute`, before `--strict` and before `plan.Compute`, the engine groups a module's gated intents
by settings file and computes their digest: sha256 over each group's target, pointer and canonical
JSON, and each component's target, kind, name, canonical hooks and canonical inline servers (all as
rendered, so absolute paths are in it), then the module version's content digest. Consent is
recorded in `consent.json` under `<module>/runtime-hooks/<sha256 of the settings file's absolute
path, symlinks resolved>`, which cannot collide with an install hook entry because module names have
no `/`, and gives each project and scope its own consent. A recorded digest, a
`--trust=runtime-hooks` or a yes from the CLI's prompt (each recorded after the run's receipt is answered
yes, before the first write) keeps the intents; a dry run makes
the same decision without asking or recording; otherwise the intents are dropped with
`<target>/runtime-hooks-declined`, per settings hooks and per component. Dropping them is what makes
consent fail closed: the planner sees the module's installed groups and components as orphans and
removes them. The consent view (`engine.RuntimeHook`) reads handler fields by exact name, as Claude
Code does, shows every handler or matcher-group field it does not describe as JSON, and passes every
string through `untrusted.Text`. An inline server's view entry has `mcp_server` set and no event; it
shows env and header values, since they change what runs.

**Drift.** A hook group is identified by its content, so an owned group missing from its settings
file is one the user edited or removed. `plan.Compute` keeps a missing owned non-string member owned
but absent (step `absent`) instead of writing it again, which would run both copies; the engine
reports `<target>/runtime-hook-drift` as info with a hint (it records the user's choice, so
`--strict` ignores it, and human output prints info findings that carry a hint), and `--force`
writes it again. On uninstall the member is forgotten and the drift is info. String members and
files keep the "was missing; rewritten" rule.

## Applying

`plan.Apply` runs under the scope lock: `$XDG_STATE_HOME/lola/locks/<hash>.lock` for a project,
named after the resolved `.lola/` path, or `$XDG_STATE_HOME/lola/.lock` at user scope. The lock
lives outside `.lola/`, so removing an empty `.lola/` never unlinks a lock another process is
waiting on. Apply stages every new file next to its destination, moves each existing destination
aside, renames the staged files in, writes the ledger, and then, when the command changes them,
each manifest in turn (`project.yml`, then the project profiles a sync writes pins into, Plan 6b
§6.4): staged next to it (at a symlink's target), the current one backed up with a hard link (a
copy where links fail) and left in place, the new one renamed over it, and the directory synced,
so a crash never leaves the scope without a manifest. The ledger and manifest writes are part of
the transaction: any failure up to and including them restores files, ledger and manifests in
reverse order. A `ManifestWrite` may carry a `Check`, run just before it is staged: a profile's
(`Profile.Unchanged`) refuses the apply when `.lola/` or `.lola/profiles/` stopped being the
project's own directories, or the file no longer holds the bytes the run read (ruling R-6b3a-4). Afterwards backups are removed, emptied directories are pruned (never
past a target's base directories), and a logical symlink whose target was deleted is removed;
the `after` callback then runs.

The manifest's bytes come from `manifest.Edit`, run under the scope lock before `Apply`: it
reads the file again (refusing one that does not load) and edits the yaml.v3 node tree, touching
only the entries and fields the command changes and appending new entries, so comments and order
survive. It decodes and validates the bytes it produces as `manifest.Load` would, so nothing it
could not load again (a commit from a corrupt cache index, say) is written. The file is encoded
again (yaml.v3, two-space indent), so blank lines between entries are dropped and indentation is
normalized. `manifest.EditDocument` is that node edit alone; `profile.Profile.Pin` uses it for a
project profile's pins, on the bytes the run read and checked (refusing a file that changed since,
§19 S6), and checks the result loads as a profile. `manifest.Load` and `ledger.Load` check every recorded source, ref, path, commit and
digest (`source.Recorded`) and refuse text that would not print as it is (`untrusted.IsPlain`),
before anything reaches git; `manifest.Load` reads through `yamldoc.Read`, the reader
`config.yaml` and `selections.yaml` use (non-blocking open, regular files only, 1 MiB, one
document, no anchors, aliases or merge keys), and reads `version` before the strict decode.

`.lola/` itself is created when the scope lock is taken; its `.gitignore`, which lola owns, is
written when a scope is committed, so a refusal or cancel never touches a `.lola/` that
already existed (the Python lola's, for example). On
release `.lola/` is removed only if this command created it and nothing was recorded (no
`project.yml`, and an empty or absent ledger), so a failed or empty first run leaves no trace.

## Fetching sources

`source.Parse` classifies an argument: a URL (`://`, but `file://`) or git's scp form (a Windows
drive path excepted) by its form alone, never looked up on disk, where a directory a checkout
names like it could stand in for it (Plan 6b ruling R-6b3b-5); anything else by what is on disk.
`source.Fetch` stages a module from a `Spec`. Both call one
unexported `validate`, because the engine also builds `Spec`s from the ledger and the cache index
without parsing an argument, and a project's files may be someone else's. `validate` refuses
`-`-prefixed locations and refs, remote-helper syntax, `git://`, credentials, queries and fragments
in URLs (`file://` included), plain `http` off loopback, and refs that fail `git check-ref-format`
(full SHAs skip that check). A user name is allowed only in `ssh://` and scp form, a password never
is, and an scp location with `user:password@`, `?` or `#` is refused. A local archive must be a
regular file, checked with `Lstat` before any open, so a pipe cannot block lola.

```
git      → clone into scratch ──────────── listing: git ls-files in the clone
archive  → extract into scratch ─────────┐
URL      → download → extract ───────────┴ listing: directory walk (never git)
folder   → (in place) ──────────────────── listing: git ls-files in a work tree, else a walk
            ↓
         module root → copy (ignore list, no symlinks) → Fetched{Dir, Name, Commit, Executables}
```

**Run staging.** Every engine operation that fetches (`InstallAll`, `InstallChoices`, `Fetch`
for `cache add` and Add a source…, `Update` and `Render`; `migrate` goes through `InstallAll`)
creates its own `run-*` under `$XDG_CACHE_HOME/lola/.staging/` (mode `0700`, `Engine.runDir`)
and removes it on every return path. All but `Fetch`, which reports no notices, first remove
`run-*` directories last modified more than a day ago whose lock no live run holds
(`Engine.sweepRuns`, A28); the sweep is best
effort, and what it cannot remove is a warning notice, never a refusal. Everything
temporary goes inside: fetch scratch (`scratch-*`), fetched copies (`fetch-*`), the
reachability check's repository, version builds (`version-*`, assembled beside the staged tree
by `Store.Put`) and snapshots. A process killed outside lola's control leaves its directory
until the next sweep. The sweep goes by the run directory's modification time, which changes only
when a direct child is created or removed, so a run idle for more than a day (realistically, a
command left waiting at a confirmation prompt; a fetch that long inside `scratch-*` is the
other case) can be swept; the user docs say so. A dry run sweeps too, since the directories are
lola's own temporaries and the sweep precedes the dry-run check (`InstallAll`, `Render`).

Each remote kind produces a local tree in a scratch directory in the run's directory, which is
removed on success and on error. The copy that every kind shares then runs over the
tree with an explicit **listing mode**: it never guesses for a fetched tree, so a `.git/`
inside an archive is never handed to git, and an archive extracted below the user's own
repository is never filtered by that repository's ignore rules.

- **git.** Every git command starts with `-c core.hooksPath=/dev/null -c
  protocol.ext.allow=never -c core.fsmonitor=false -c core.autocrlf=false` and empty LFS
  filters; user input appears only after `--` or as `--branch=<ref>`. Clones use
  `--no-checkout`; before any checkout lola writes `$GIT_DIR/info/attributes` (`* -text -eol
  -filter -ident -working-tree-encoding`, which outranks `.gitattributes`) and lists the tree
  with `ls-tree -r -z`: a mode `120000` entry inside the module path (`--path`, or the whole
  tree without one) or on the way to it is refused (Windows without `core.symlinks` would check
  it out as a plain file; links elsewhere never reach the module), as is a `--path` that is no
  directory of the tree in its exact letter case (a case-insensitive checkout would find it),
  and `100755` entries are the version's executables. Then `reset --hard` (or
  `checkout --detach` for a SHA) writes the bytes as committed. The environment sets
  `GIT_TERMINAL_PROMPT=0` and `GIT_PROTOCOL_FROM_USER=0` and drops the variables that select or
  configure a repository (`GIT_DIR`, `GIT_CONFIG_PARAMETERS`, …); the folder fetcher's
  `git ls-files` uses the same prefix and environment. A ref resolves through `git ls-remote`
  with full ref names, matched exactly. A full SHA that names no branch or tag is fetched with
  `init --object-format`, `fetch --depth 1` and `checkout --detach`; clones run with `-q`, so a
  failure shows git's errors, not its progress. A remote command whose stderr says access was
  refused (in a `fatal:` line: `could not read Username`, `repository … not found`,
  `Authentication failed`; or ssh's `Permission denied (…)`; `remote:` sideband lines are never
  read) is a `source.AccessError` instead (`accessDenied`): lola's message for the location after
  `--`, with `git ls-remote -- <location>` (plus `-c http.followRedirects=false` when the run had
  it, and `ssh-add` when the server takes keys) as hints, none for a location that is not plain or
  over `MaxName`; callers keep them (`unhinted` passes it through). One 5-minute context covers the fetch;
  `WaitDelay` (5 s) makes git stop on time even when a child keeps its output open, and a
  timeout is coached by the location's transport (ssh prompts, or the network for https). git's
  stderr, which carries a remote's `remote:` lines, goes to a `headWriter` that keeps only its
  first 4 KiB and discards the rest, so a chatty or hostile remote cannot grow lola's memory.
- **Archives.** Extraction writes through an `os.Root` opened on the scratch directory, so no
  entry resolves outside it even if a name check has a bug. Only regular files and directories
  are created, files with `O_EXCL`; every entry is copied through a reader capped at its
  declared size plus one byte. The entry, byte and ratio limits are constants that specs lower
  through an unexported parameter. Before `archive/zip` parses a zip's central directory into
  memory, `checkZipDirectory` finds the end record (and the zip64 one) the way `archive/zip`
  does and refuses a declared count over the entry limit, or a declared size or actual span
  over 1 KiB per allowed entry: `archive/zip` reads records until one fails to parse, whatever
  the declared size, and checks the count only modulo 65536.
- **Downloads.** A clone of the default transport (proxies honored) with compression disabled, a
  2-minute timeout, at most 5 redirects, none from https to http and none to http off loopback, and
  a body capped at 256 MiB.
- **Untrusted text.** git's stderr, archive entry names, module-root candidates, HTTP status text
  and echoed URLs go through `untrusted.Text`, which replaces control characters, invalid UTF-8,
  Unicode format characters (bidi overrides, zero-width characters, BOM) and line or paragraph
  separators with `?`, indents continuation lines, and caps the length. A parsed URL is echoed as
  scheme, host and path only (`displayURL`); one that does not parse, or was never checked, is
  echoed up to its first `?` or `#`, and only as its scheme when it holds an `@` (`redactURL`). The
  redirect target of a failed download and refused `git://` URLs are not echoed at all; OS errors
  lose their path.

Every fetch reports its **executables** from the source's own metadata: git's tree modes, an
archive's entry modes, a folder's file system. The digest takes execute bits from that list,
never from the file system lola copied to, so a platform without execute bits digests the same
content the same.

**Reachability.** A pinned git commit must be reachable from the entry's ref:
`source.CheckReachable` resolves the ref as a fetch does, fetches its history with
`--filter=tree:0` into a separate, freshly initialized scratch repository with
`GIT_NO_LAZY_FETCH=1`, and asks `git merge-base --is-ancestor`; a commit that is absent or not an
ancestor is refused. It is skipped when the ref is the commit itself.

## Cache

```
$XDG_CACHE_HOME/lola/modules/<name>/
  index.yml                # source, type, path, heads: [{ref, digest, commit}],
                           #   last_used: {<digest12>: YYYY-MM-DD}
  versions/<digest12>/
    content/               # the normalized module: native layout + one generated lola.yml
    .lola-origin           # format, manifest, others_present, ref, commit, fetched_at,
                           #   digest, executables
```

The digest is sha256 over, for each regular file under `content/` in byte order of its slash
path, `path NUL octal(mode) NUL decimal(length) NUL content`, where mode is `0755` for a path in
the version's `executables` and `0644` otherwise (the generated `lola.yml` never is). A change to
its input, or to the bytes a fetch and `module.Normalize` produce for unchanged content, bumps
`manifest.Version`; a golden-digest spec guards it. A head records the commit its fetch
resolved; `.lola-origin`'s `commit` is only the first fetch's, which may be another commit with
the same content. Putting content the cache already holds rewrites the version's `executables` when
they differ, so a version cached before execute bits were recorded is repaired by the next fetch
of it (`cache update`, or `install` of its source).

`cache add` detects the staged module's format and reads it (`module.Detect`), normalizes it
(`module.Normalize`: one generated `lola.yml`, every other lola manifest removed), digests the
result, and renames the version into place in whole from the run's staging directory under
`$XDG_CACHE_HOME/lola/.staging/` (same filesystem), under `$XDG_CACHE_HOME/lola/.lock`.
`.lola-origin` records the detected format and which manifests were read and ignored. Module
names and version ids read back from the cache are validated before they are used in a path; a
head's commit must be empty or a full SHA, and a version's recorded digest must be the one its
id abbreviates, or the cache coaches `lola cache rm`. The IR is re-derived from `content/`,
never stored.

Installs run from their module copies, so the cache is re-fetchable. `render` installs the version
the ledger records, never a head, so `cache update` changes no installation: for a git or archive
module `Store.Pinned` copies that version's `content/` into a **snapshot** in the command's private
`run-*` staging directory, hashes the copy with the recorded executables, and loads the module from
it, so the IR, the intents and the module copy all come from the bytes that were hashed. On a miss
(after `lola cache rm`, say) `fetchPin` fetches it again from the cache's recorded source or the
ledger's: a git commit must be reachable from the ref and is fetched by SHA; the content is prepared
in the run's staging (`Store.Prepare`) and must declare the module's name (`PutRequest.Name`) and
have the recorded digest, or nothing is cached; then render commits it (`Store.Commit` with
`headIfAbsent`, so the head for the ref is set only if it has none) and renders from the staged tree
it hashed. A folder module is read from its folder (the version installed when the folder is gone),
and refused, with nothing cached, when it now declares another name. When the version cannot be
obtained (a git entry with no recorded commit, a fetch that serves another commit, other content
than the recorded digest) the error carries two hints: `lola install <source> [--ref r] [--path p]`
(`installCommand`), which installs what the source serves now, and `lola uninstall <name>`.
`install` pins git and archive entries (`commit` from the fetch or head, `digest`); folders are
never pinned. `status` reports `stale` when the manifest's pin differs from the ledger's digest, and
`update_available` when the cache head for the entry's ref differs from the pin (or from the
ledger's digest when unpinned).

`last_used` is the UTC day a run last used each version (Plan 9 §6.3). `Store.Commit` writes it
for every version it commits, `Store.Pinned` for the version `render`, `sync` and `profile` read,
and `InstallAll` (`Store.Used`) for the version each request resolves, outside a dry run, so
`lola info`'s plan records none. `Store.Used` reads the index without the lock and, only when the
day differs, takes `.lock`, reads it again and writes it: at most one write per version per day.
The store's clock is the engine's `Now`, read at each use, so specs fake the day. Recording is best
effort: `Store.Pinned` and `InstallAll` go on when `Store.Used` cannot lock or write the index (a
read-only cache). An entry read back whose id or day is not one lola writes is dropped, never
refused (R-9-44): its key reaches no path, and its version counts as first seen by the next clean.

Each staging command's `run-*` directory under `.staging/`, and `lola check`'s `check-*` under
`tmp/`, is held by an exclusive flock on `<dir>.lock` beside it for the run's life (R-9-9,
`scratch` in `internal/engine/staging.go`): the lock file is created and locked before the
directory exists, and the run removes the directory, then releases and removes the lock. A lock
file another lola removed between its creation and its locking is detected (`os.SameFile` of the
locked descriptor and the path) and another name is taken. `sweepRuns` removes a `run-*` older
than a day only when it can take its lock without waiting, so a run waiting at a prompt for longer
keeps its scratch (A28).

`lola cache clean` is `engine.PlanClean`, which writes nothing, and `engine.ApplyClean`, which
applies the plan the user confirmed (`internal/engine/clean.go`). The plan lists superseded
versions (no head points to them), with an age (`--unused-for`, else `config.yaml`'s
`cache_unused_for`) the versions unused that long, heads included, and the scratch whose lock it
can take without waiting; `--all` lists every entry of `modules/`, `markets/`, `.staging/` and
`tmp/` but held scratch. A module whose `index.yml` it cannot read, or an entry it cannot read,
is kept as `unreadable` and planning goes on. Apply takes the markets lock (for `--all`), then
the cache lock (`Store.Lock`) for its whole removal. Under it, first, every item is checked again:
every directory between the cache's root and it is still a real directory (`realDirs`, `Lstat`),
it is still there, a superseded version still headless, an unused one still unused; one that
changed is kept and reported, as is every item of a module whose directory is not real (one
check per module decides both its items and its index); a module whose index it cannot read
again, or one of whose versions it cannot examine, is kept as `unreadable` with all its items and
its index unwritten, and the clean goes on; `.staging`, the rename target, must be a real
directory too. Then, before any rename, each module's index drops the heads and `last_used` of its
versions that go and dates every version without one (`Store.Cleaned`), so a kill leaves no head
naming a removed version; an index write that fails, or a rename, puts back, from the index read
before, the heads and `last_used` of the versions not known to be gone (`Store.Restore`), and a
failed rename's error is a `CleanError` whose JSON carries `removed` and `bytes_freed`. Then each item is renamed into a
`.staging/clean-<x>/` of its own (same file system); a scratch item's lock is held across the
rename of its directory and released before its lock file is removed (Windows refuses to remove
an open file), and a lock alone is held across its own rename except on Windows. A module whose
last version went is renamed aside whole. At the end the `clean-<x>` directory is deleted;
`bytes_freed` subtracts what is left of it, which is a warning notice that the next clean
retries, as it removes a `clean-<x>` a killed clean left. Module names and version ids
read from disk are validated before they are joined into a path; every listing uses `os.Lstat`,
so a link is removed as a link and never followed, and sizes count regular files only. A `path`
is shown as it is on disk when it is plain text, which every validated name and every name lola
makes is, and through `untrusted.Text` on one line otherwise. No other path holds the cache lock
while it takes the markets or profiles lock (the cache lock is taken only inside `Store`), so the
markets-then-cache order cannot deadlock. A reader that loses a version mid-copy fails its digest
check, and its rerun fetches the version again, as after `lola cache rm`.

Attribute files (`internal/module/attrs.go`) are untrusted data: read as regular files under 64 KiB,
YAML is built from `yamldoc.Decode`'s node tree by tag (only `!!str`, `!!bool`, `!!int`, `!!float`,
`!!null`, sequences and mappings; a float must be finite, and a plain number yaml.v3 leaves a string
because `ParseFloat` refuses it, such as `1e999`, refuses too), JSONC is `fsx.StandardJSON` (hujson,
comments and trailing commas blanked so offsets stay, nothing re-encoded) read token by token,
refusing a key given twice; one `attrRule` caps keys, strings, items and nesting in both. Vars are
read the same way (`module.ParseVars` from `lola.yml`'s node and, through `manifest.EntryVars`, from
each entry); `planModule` checks an entry's overrides against the declaration (`checkVars`) before
anything renders and binds the declared vars with the overrides (`resolvedVars`).

## Trust kinds

`--trust[=kind,…]` is one flag (`internal/cli/trust.go`, `trustFlag`) on every command that can
ask a consent question; `engine.TrustKinds` (`internal/engine/trustkinds.go`) is the set, a bit per
kind in canonical order (`entries`, `profiles`, `scripts`, `hooks`, `runtime-hooks`), and
`CommandTrust` the kinds each command asks. `ParseTrust` turns the flag's values into kinds before
any network contact: a bare `--trust` reaches it as `TrustBareValue` (pflag's `NoOptDefVal`) and is
`entries,profiles` within the command's kinds; `all` is the command's kinds; an unknown, empty or
untaken kind is a `TrustFlagError` whose `Fix` is the kinds of the run its hint offers. pflag gives
a `NoOptDefVal` flag its default unless the value is joined by `=`, so `--trust hooks` is bare
`--trust` and an argument: `trustFlag.Set` records how many positional arguments pflag had read
(`FlagSet.Args()`), and `parse` refuses a kind-named argument at that index, unless after `--`
(`TrustArgument`). `TrustKinds.Arg` writes kinds after `=`, never `all`, and every hint writes it.

`Options.Trust` covers items as they are planned: sync's trust report by kind (`TrustReport.split`,
`consents`), install hooks (`allowed`), runtime hooks (`runtimeHookConsent`), and a remote
profile's `--add` or `--update` (`consentRemote`). A covered item goes to `Result.Trusted` (a
`Consent`: kind, item, `new` or `changed`) and, for consent.json, a pending `grant`; an uncovered
one is asked as without the flag, its yes a pending `grant` too (A23), or refused or declined (`Result.needs` keeps the kinds that would
refuse, for a dry run's command). `confirmTrusted` runs once per engine run before the first write
(`InstallAll` before `BeforeWrite`, `execute`, `Sync` before the apply): it shows the items to
`Options.Receipt` (No is `ErrDeclined`) and only then writes the grants, the flag's and the
terminal's alike (A2, A23); entries and profiles are recorded in trust.json with the apply, as
before. A later failure of the run is a `TrustedError`, whose JSON carries the grants written
(A22). The CLI's receipt prints them grouped by kind
on stderr and asks `Proceed?` on a terminal unless `-y`; under `--json` it prints nothing, as
`trusted` carries them. `Options.Rerun` is the user's run for a given set of kinds: sync builds it
(`commandOf`), the CLI passes `runWithout` for install, render and migrate, and a refusal for want
of trust hints it with the kinds given plus the missing one.

A command that runs the engine more than once shows one receipt (R-9-5): each run gets a
`TrustBatch` in `Options.Batch`, which takes its items and grants at its receipt and stops it with
`ErrBatched`; the command asks the one receipt, then the runs go again, asking nothing, each with
the batch in `Options.Expect`: it records its own grants just before its own apply, when each is
one the batch holds by key and digest (entries and profiles items by label), else it fails with
`ErrTrustChanged`, writing nothing (R-9-16), so one module at two versions in two scopes records
each digest before each apply. A terminal yes to an install or runtime hook in the first run is
one of the batch's grants: the second run, which asks nothing, takes a hook whose key and digest
the batch holds as consented (`allowed`, `runtimeHookConsent`) and records it the same way, while
a hook changed in between is refused as one never answered. `lola migrate` does so per
destination; `sync --scope both` and
`profile --update` (with `--trust`) also replay each scope's trust and removal answers in the
second run (`syncPass`). A failure carries what was recorded so far as `trusted`
(`syncedScopesError`, `updateError`, migrate's `TrustedError`). `profile --update` keeps the profiles it
accepts staged in the engine (`Engine.staged`, read by `loadProfiles` in place of the files) until
`CommitStaged` writes them after that receipt, in the order they were asked about, each refused
when its `source.json` changed since it was read; a refusal after a write stops the run as an
`UpdateStoppedError` that names what was written and applies nothing. `trustFlag` gives a bare `--trust` a value with a
NUL byte (`TrustBareValue`), which no argument can hold, so `--trust=entries,profiles` is never
taken for it; help shows `[=entries,profiles]` or `=<kinds>` through an annotation
(`trustShownKey`, `aliasedFlagUsages`).

## Trust record

`project.yml` is committed and someone else's after a pull, so lola keeps its own account of what it
wrote there. The **view** of a manifest is what can change what lola fetches or writes: the
top-level `targets` and each entry's `name`, `source`, `ref`, `path`, `commit`, `digest`, `targets`,
`rules`, `market` (Plan 6c §8: shown in the diff as `market (as written)`, through
`untrusted.Text`, never checked), `components` and `vars` (Plan 7 §8: what its templates render
with; shown as `name = value` pairs through `untrusted.Text`, compared by their JSON, so a list
`trust.json` decodes as `[]any` equals the entry's), modules sorted by name, empty values omitted.
`EntryOf`, `equal`, `clone`, `take` and `fieldChanges` each list the fields by hand. `trust.json`
(`$XDG_STATE_HOME/lola/`) maps each project key (`paths.ProjectKey`, compared with `SameKey`) to its
view and the hex sha256 of the view's canonical JSON. An entry is **covered** when the record holds
an entry of its name with every view field equal. Top-level `targets` are recorded; `lola sync`
shows a change to them (`trust.Check`), an install refuses to take uncovered ones unasked
(`untrustedTargets`, set per scope in `InstallAll` when no `-t` is given; the pickers get them as
`ScopeChoices.UntrustedTargets` and ask instead), and `render` leaves out a module that installs to
them. A project with no record covers nothing: there is no seeding from `lock.json` (user ruling,
2026-10-13: lola-go is unreleased, so no older project needs it). User scope has no record;
`user.yml` is the user's own, and the engine never loads one for it.

A project's profiles (`.lola/profiles/<name>.yml`, Plan 6b §8.1) are committed too. The view
gains `profiles`, a map from each profile's name to its **part**: its entries, sorted by name,
each with the targets it installs to (its own, else the profile's), so a change to a profile's
`targets` is a change to every entry it gives them to. `profiles` is omitted when empty, so a view
without profiles hashes as in 6a. `View.Part(key)` is one file's part (`""`: `project.yml`'s
targets and entries) and `WithPart` puts one back; `Check` compares a view with the record's
`project.yml` part only, and a profile is checked against a record holding its part.
`Accept` and `Refresh` take the key of the part they change and carry every other part as it is
(§19 R2), so trusting one profile's entries never touches `project.yml`'s record or another
profile's.

- **Reading.** The version is decoded on its own first, as `manifest.Load` reads `project.yml`'s: a
  newer one is refused with the hint `upgrade lola` (not for a key the newer lola added) and a
  negative one as unreadable. Then strict JSON with unknown keys and trailing data refused, at most
  4 MiB, and each record's hash checked against its view. A file that fails is an error with a hint
  and is never overwritten. The file and `$XDG_STATE_HOME/lola` are refused when a symbolic link or
  (outside Windows, whose permission bits mean nothing) group- or world-writable; the opened file
  must be the one checked (`os.SameFile`). Go reports a Windows junction as irregular, so a state
  directory that is one is refused as not a directory. A version up to this lola's is read, 0
  included, as `consent.json` reads its own.
- **Writing.** Under `$XDG_STATE_HOME/lola/locks/trust.lock`, taken after every scope lock and
  held only around the read-modify-write: the file is read again and only this project's record
  replaced, then written `0600` through `fsx.WriteFileAtomic`. The state directory, `locks/` and
  the lock file get the read checks before the lock file is created, and a save that would make
  the file larger than the 4 MiB a read accepts is refused, which leaves the file as it was.
- **Refresh.** Every manifest write goes through `engine.saveManifest(sc, loaded, change, rec,
  apply)`: it edits the manifest, hands the edit to `apply` (the commit, which stages it with
  the ledger), and after a successful apply at project scope saves `trust.Refresh(record, "",
  view(loaded), view(loaded.Applied(change)))`. Every field the write changed takes its new
  value, an added entry is recorded whole, a removed one is dropped, and every other field keeps
  its trust state: a write never covers a field it left alone, and a field changed in an entry
  the record does not hold is not recorded. `loaded` is a clone of the manifest read under the
  scope lock. A failed save leaves lola's own edits uncovered (fail closed) and the error says
  what the caller applied (an uninstall says it removed what was asked), and which scopes of the
  same install it then did not reach. An apply that writes no manifest or profile (an uninstall
  of some targets of a module `project.yml` does not list) saves no record: it has nothing new to
  cover.
- **Accept.** A sync records what the user accepted with `trust.Accept(record, "", view(loaded),
  modules, targets)`: the entries named take their view in the loaded manifest (an entry the view
  lacks is dropped), the top-level targets when `targets` is set, and every other entry keeps its
  record. Accepting every change and the targets returns the view itself, which is what an
  all-or-nothing answer records; a partial answer, or a run limited to some modules, names fewer.
  With pins to write, `saveManifest` refreshes `&Record{View: Accept(…)}`, so only the accepted
  entries' pin fields move.
- **Refusals.** Over an uncovered entry: an install by name (in `InstallAll` and `InstallChoices`,
  checked for every argument at every scope before any is resolved, so nothing is fetched), an
  install that adds targets (`lola migrate`'s), and an uninstall that keeps some of the module's
  targets are refused (`notTrusted`, wrapping `ErrNotTrusted`; the message, from `untrustedWhy`,
  says "changed for it since you last trusted it", "added it since you last trusted the file" for
  an entry the record never held, or "has not been trusted on this machine" for a project without
  a record) with the hint `review and apply it: lola sync <m>`. 6a.2 and 6a.3a hinted installing
  from the source, which re-pinned to what the source served and skipped sync's review; that
  helper is gone. An install by source writes the entry as new: it
  inherits no `targets`, `rules`, `ref` or `path`, and `planInstallModule` drops the entry from
  `loaded`, so the refresh records it whole. With no targets given, the module keeps the targets
  `lock.json` installed it on (the user's own installs chose them; a ledger git tracks is refused,
  see below), so what is installed is what the entry records. An argument that names an uncovered
  entry is refused as a name even when `source.Parse` would take it as a folder: `resolve` takes a
  cached name first and applies the entry's ref to any name-shaped argument, and the pickers' fetch
  caches the folder under that name, so only `./<name>` is unambiguous. Removing a module is always
  allowed. The pickers never offer what the install would refuse after the summary: an explicit name
  is refused at listing, a cached module with an uncovered entry is left out under one notice per
  project scope (`uncoveredEntries`), and
  `moduleScope` shows a source over an uncovered entry as the new entry it becomes. Add a source…
  caches the source and lists it by name, so an uncovered entry stays out of the pickers (S2 applies
  only to a source argument), and Change scope onto a scope where a kept pick is uncovered is the
  refusal, not a dropped pick. `lola migrate` reports the module as unmappable with the refusal's
  reason and hint; it refuses after `resolve` has fetched the source, which is the user's own legacy
  configuration.
- **Tracked ledger.** At project scope every operation that decides from the ledger runs
  `refuseTrackedLedger` after loading the scope and before resolving or fetching anything:
  `Render`, `InstallAll`, `InstallChoices` (so the pickers refuse what the install would) and
  `Uninstall`; `Status` (`lola list`) only reads and does not. It runs `git ls-files
  --error-unmatch -- .lola/lock.json` through `GitCommand` (`ledgerTracked`): tracked is refused
  (hint `git rm --cached .lola/lock.json`, as `git -C <root> rm --cached …` unless run from the
  root, and none when `untrusted.Text` would change the root), a git failure in a work tree is
  refused (fail closed, with a hint to make `git status` work there; `dubious ownership` is the
  common cause), and git missing from `PATH` is a warning notice (a picker warning in
  `InstallChoices`). This is what makes the ledger this checkout's own, which the
  install-by-source targets above and every removal rely on.
- **Canonical locations.** `source.Parse` resolves a folder or local archive with
  `filepath.EvalSymlinks(filepath.Clean(p))`, once (an archive link whose target has a different
  archive suffix is refused, since the suffix picks the extractor); `install` records a relative
  source against the project root with its links resolved (`canonicalRoot`), and every reader of a
  recorded relative source (`sourceRoot`: sync's validation, render's checks, `recordedSpec`)
  resolves it against that root too, so it resolves however the root was reached. The cache's
  same-source checks accept a location an earlier lola recorded through a link
  (`source.SameLocation`).
- **Hints.** Every `lola sync` command lola prints, in a hint, a notice, a dry run's closing lines
  or the picker's equivalent command, comes from `Engine.SyncCommand` (built on `Command`, which
  every other scoped command in a hint uses too): `lola sync`, then the scope's flag
  (`RootArgs`): `--user` at user scope, `--project` when `FindProject` of the working directory
  (`Engine.Getwd`, `os.Getwd` unless a spec sets it) is the scope's root (`FoundHere`, which
  install's equivalent commands use to decide `-C`), else `-C <root>`; a command without a scope
  flag would ask for its scope on a terminal (Plan 6b §12.3). Then the names and flags, each through
  `untrusted.Text` and `errs.ShellJoin`. A hint that repeats the user's run carries its names,
  `--update` and `--force`, and `--trust=` the kinds given (`SyncRequest.rerun`); the
  "… and N more" of a capped file list or dry-run plan names the user's run with `-n --json`
  (`Engine.ListAll`). An install's `retry with: lola install <names>` (`withRetry`) is built per
  scope with `Command`, and left off a refusal for want of trust (`ErrNotTrusted`, the untrusted
  top-level targets) or for conflicts (`plan.ConflictError`), whose own hints are the way forward.
  A conflict refusal that `--force` overrides hints `Options.ForceRun` (`plan.Request.ForceRun`):
  the CLI's `forceRun`, the user's run as typed with `--force` added. One it cannot override hints
  nothing from the plan; `ownedConflicts` (in `prepare`) says what frees each destination another
  module owns (`Conflict.Owner`): the owner's `lola uninstall` at the scope, keeping
  `Options.Display`, hinted only when the install's command line named the claimant
  (`Options.named`); else, in the reason, removing the claimant from the layer that lists it (its
  `ledger.Entry.From`). The other reasons (one module providing a destination twice, two of one
  run) say what to do in the message. The refusal of no scope hints `ScopeRequest.Run`, the run
  without its scope flags (`scopeFlags`), plus each scope flag the command takes; install's
  no-assistant refusals hint the run plus `-t <id>`, and a project at the home directory
  `InstallSpec.UserRun`. Every such rerun drops `consentFlags` (`-y` and `--trust`:
  rulings R-6b3c-8, R-6cux-4), and `runWithout` repeats a bool only as given (`--x=false` against
  a true default, nothing for a false one). `confirm`'s refusal off a terminal hints the run with
  `-y`. `lola migrate` needs no case of its
  own: its process's directory decides, not the `Cwd` it passes.

## Layers

What a scope wants is a stack of **layers** (`internal/engine/profile.go`, Plan 6b §6.2), lowest
first: at user scope `default` (when `profiles/default.yml` exists), the active profiles in
`lock.json`'s order, then `user.yml`; at project scope the active profiles, then `project.yml`
(`user.yml` is never a project layer, D6). `Engine.layers` resolves the active list against
`Engine.visible` (`profile.Visible`, and at project scope `profile.Project`) and reads each
profile once per run (§19 S6); a stored name with no file is a
warning naming `lola profile <scope flag> <name> --off` (built by `Engine.Command`, e.g.
`lola profile --user user:go --off`) and counts as off, and a profile that does not load
refuses the run. A profile's relative local sources are resolved against its directory (§19 R5)
before anything else sees them: made absolute for the user's own profiles, so `lock.json`
records them absolute, and joined to `.lola/profiles` for a project's, so they stay relative to
the root as `project.yml`'s are. `desired(layers)`
walks the layers in order; a later entry replaces an earlier one of the same name whole and,
when they differ, adds a notice. Each `Wanted` carries its entry, `From` (the layer's name:
`project.yml`, `user.yml`, `profile:user:go`, `profile:default`) and `Over` (the layers it
replaced).

lola never writes a profile of the user's own (D7, §19 R1): `pinsOf`, `Pins` and `needsPin`
cover the manifest's entries and project profiles' only (`syncEntry.own` is the rest). `validateSync` gives a profile's unpinned git or archive entry the commit and
digest `lock.json` records for the same source, ref and path, whichever layer won before
(`syncEntry.ledgerPin`; `from` is not compared, since a planted `lock.json` sets it too), so it installs as a pinned entry does until `--update` resolves its ref
again; the version installed is what `planSync` records anyway. At project scope such a pin is
checked like an untrusted entry's: its commit must be reachable from the ref even on a cache
hit, since an untracked `lock.json` planted in the checkout could name any commit (§11, S5). `--update` naming a profile's pinned entry is refused with
`edit the pin in <path>`; an unlimited `--update` leaves such entries at their pins with an info
notice. A pinned fetch's error names where the pin lives (`pinnedIn`).

**Project profiles** (`.lola/profiles/<name>.yml`, Plan 6b §4.1, §8.1) are committed, so their
layers (`Layer.Key`, the profile's name) are read with `profile.LoadProject` and treated as
`project.yml` is, not as the user's own: a sync's `syncEntry.key` marks their entries. Coverage is
the profile's part of the trust record (`rec.View.Part(key).Covers`); `containLocalSources`
checks their local sources against the project (§19 R5); a covered folder's content is read and
compared like a manifest folder's; an unpinned git or archive entry is pinned, with the user's
trust, and the pin written back into the profile (§19 R1), and `sync --update` re-pins it. The
trust check (`trustParts`, `checkParts`) covers `project.yml` and every project profile of the
run's layers (the active ones and those being turned on), each with the entries of it that win
only (ruling R-6b3a-3, narrowing R2): an entry a later layer shadows is neither checked nor
accepted, and its record is left out of the check, so it is reviewed, with the content it
installs, on whatever run makes it win (a sync after `project.yml` drops the module, or an
activation that turns a higher profile off; P9's moved modules include it). Each change of a
profile names it (`trust.Change.Profile`, `project:<name>`), and every other part of the record
is carried (§19 R2). `saveTrust` drops, on every save, the parts of profiles whose file is no
longer in `.lola/profiles` (R-6b3a-5), never reporting them removed. Any project-scope run with
an active profile lists `.lola/profiles`, so a symbolic link there refuses it even when only the
user's own profiles are on. A trust answer is per module: `acceptance.modules` holds the
accepted entries per part, and `acceptance.record` accepts them part by part. `saveManifest`
takes the profiles' pins too (`profileEdit`): `Profile.Pin` edits the bytes the run read, the
apply writes them with `project.yml` and `lock.json`, and the record is refreshed per part.
`render` checks a project profile's winning entry against its part as it checks a manifest entry. `SyncNames` (completion)
leaves project profiles out of the layers, since it runs no git and a committed `lock.json` could
name them; `InstallCoaching` refuses a tracked `lock.json` before it reads them.

A project profile with the name of an active `user:` profile replaces it only through an
activation that names it (D8, §4.4, ruling R-6b3a-1): `--on project:go` puts it in `user:go`'s
place in the list (`activeList`), and `--switch` naming it drops `user:go`. `replaced` finds
these pairs between the old and new lists, and `replacingOf` adds them to the activation's trust
report (`TrustReport.Replacements`, with the modules each changes); a pending replacement forces
the trust question, which for an activation is all or none (P10). `lola sync` never swaps them:
`syncLayers` keeps the list and adds `replacementNotices` (`project:go also exists → lola profile
project:go --on`), as activations and the listing do. Nothing about a declined replacement is
stored.

**Remote profiles** (`$XDG_DATA_HOME/lola/profiles/<name>/`, Plan 6b §4.2, §8.2) are listed by
`Engine.visible` beside the local ones (`profile.Remote`) and read with `profile.LoadRemote`.
Their layers are the user's own (`Layer.Key` empty, so `own()`): the consent was given when the
profile was fetched, so no project's trust view holds them (D5); every entry is pinned, so
nothing is written back, and `ownPin` coaches a pin problem with `lola profile remote:<name>
--update`. Local beats remote (decision 4): `resolveProfile` looks for a bare name among the
local profiles first, refuses a name that local profiles of two or more kinds define
(`ambiguousProfileError`, every candidate named), and takes the remote profile only when no
local one has its name; `shadowing` names the local profile that shadows a remote one, which
`activeList` refuses to turn on and listings report (`ProfileRow.ShadowedBy`). An active
`remote:<name>` whose name a user profile now has is read as that profile (`userShadow`, D8),
with the notice `user:go now replaces remote:go`, on every run: the stored name is never
rewritten, so the remote profile is used again once the user's file goes, and `--on user:go`
leaves the list as it is (`user:go is already on, in place of remote:go`). A project profile is
never swapped in by a sync: the `replaced`/`replacements` pairs take an active `remote:` profile
as they take a `user:` one, and `--on project:go` takes the first active namesake's place and
drops the others. `validateSync` refuses a remote profile's entry, or an extended one's,
that still parses as local (a Windows drive path, the one local form a remote source check can
miss).

**The profiles lock** (`$XDG_STATE_HOME/lola/locks/profiles.lock`, Plan 6b §10, §19 S6) is the
first lock any run takes. `readProfiles` takes it shared for every run that reads the remote
profiles (`Sync`, `Render`, `Install`, `Uninstall`, `lola list` (`Status`), `lola profile`'s
listing and state (`Profiles`, `ProfileState`), the install coaching and completion
(`InstallCoaching`, `SyncNames`), and `--add`/`--update`/`--remove` while they fetch, show and
ask), and lists them under it: when a killed writer left a directory behind (a `.new-`, `.old-` or
`.del-` directory; any other name starting with `.` is not lola's and is left alone), it lets go,
takes the lock exclusively, finishes that work (`sweepRemote`) and starts again, so a crash between
an update's two renames never makes a profile that is on look gone. A dry run takes it too, but
never sweeps, and takes none when no lola has made the lock file yet. The writers take it
exclusively (`lockProfiles(true)`) only to check again that what they showed still holds and to
write, so no question is ever asked under it and syncs elsewhere go on.

**Adding a remote profile** (`Engine.AddProfile`, `internal/engine/remote_add.go`, Plan 6b
§6.5) asks no scope (§19 P22): `ProfileAddRequest.Scope` is the scope a flag named, nil for none,
and only picks the scope the content is compared with; nil takes the first of `knownScopes`, the
project `foundProject` finds, else user scope, the read-only commands' rule; a `.lola/` in the
home directory is never that project (`IsHome`, ruling R-6b3c-9), and is skipped with a notice.
Hints that repeat the run are built by `ProfileRun`, which carries the scope flag only when one
was given, and the run's own flags (`ProfileAddRequest.Rerun`; a hint that sets `-n` or `--trust`
itself keeps the others, and `refuseKept`'s `--update` hint never `--trust`, which answered the
add's content, ruling R-6b3c-8); a found project that cannot be read refuses with the run at user
scope as one more hint. It checks
everything it can before any network contact: the name (`remote:go` taken as `go`,
another kind refused, then `remoteName`: valid, not `default`), the source (`remoteSpec`:
`source.CheckRemote`, which takes only `https://`, `ssh://` and git's scp form at a host
named by name, ruling R-6b3b-6; then `source.Parse`, which never looks a URL or scp form up on
disk, ruling R-6b3b-5; `source.Recorded.Check` and `source.Validate`), that the scope is readable
and its `lock.json` untracked (`profileScope`), and that it can ask (a nil `Consent` refuses,
naming `-n` and `--trust`). Under the shared lock it fetches only `<--path>/profiles` of the
source into the run's staging (`fetchProfiles`: `source.Fetch`, which refuses a link in it or on
the way to it, §19 S3; a missing `--path` or `profiles/` is told apart through
`source.MissingPathError.Exists`). `profile.Fetched` reads each named (or picked, `pickRemote`,
which never offers `default`) profile; `remoteContent` diffs its entries against none
(`trust.Check`) and, for each, resolves its module at its pin into the same staging
(`remoteEntry`: parsed without the disk, refused when local, `source.Validate`, `checkPinShape`,
`resolveSync`) and lists its whole content, every file, MCP server and hook as added, whatever
the scope's `lock.json` says, which only annotates it (`FilesNote`, ruling R-6b3b-7); nothing is
published to the cache. After `Consent`, under the exclusive lock, it sweeps, checks no profile
of the name appeared (`refuseKept`, which also refuses a file in its place), and `writeRemote`
writes `profile.yml` (the bytes fetched) and `source.json` into a `.new-<name>-*` directory under
`RemoteProfiles`, flushes that directory, and renames it into place, renaming any previous
directory aside to `.old-<name>-*` first and removing it after, and syncing the directory (§19
R11); `sweepRemote` puts an `.old-` directory back when nothing took its place and removes the
rest (`.new-`, and `.del-`, a removal's). Once the rename succeeded the profile counts as written:
a later failure (flushing, removing the `.old-` one, or `visible` checking for a local namesake)
is a warning notice, never a failed add or update. The run's staging is removed on every path.

**Updating them** (`Engine.UpdateProfile`, `internal/engine/remote_update.go`) takes the profiles
lock shared first (`readProfiles`, whose sweep finishes an interrupted write, so a profile left
aside is found), then resolves the profiles (`updateNames`, §19 P21): `ProfileUpdateRequest.All`
lists every remote profile (`profile.Remote`), none being a notice; otherwise each of `Names` is
resolved to a kept remote profile (`remoteNamed`; a bare name is the remote one, since only those
are updated), a name given twice taken once. The CLI refuses names with `--all`, neither, and a name
given twice, since its hints keep the flags of the run (`runWithout`), but for one naming profiles
you did not name, which drops `--trust` and `-y` (ruling R-6b3c-8); `--all` is in
the per-action flag table, so other actions refuse it. It refuses before any network contact when it
cannot ask,
and an engine call naming neither profiles nor `All`. It asks no scope (§19 P20):
`ProfileUpdateRequest.Scopes` are the scopes the flags named, none for no flag; none applies in
every scope of `knownScopes` (the project `foundProject` finds, never the home directory, then user
scope), one in that scope only (a project at the home directory, `IsHome`, is refused before the
profiles lock is taken, hinting the run at user scope, ruling R-6b3c-9), and two (`--scope both`,
which the CLI takes only when the project has `.lola/`, ruling R-6b3c-2) in both, the project first.
`ProfileRun` repeats such a run with `--scope both` (and `-C <root>` for a project not found from
here). Each of those must pass
`profileScope`, and `scopeBefore` must read what each wants (`uses`), before any network contact: a
scope it cannot read refuses there, so nothing is kept that cannot be applied, with `narrowUpdate`
hinting the run narrowed to each other scope without `-y` or `--trust`, which were
given for the run as a whole (ruling R-6cux-4; without a scope flag, the message names a found
project's untrusted changes, rulings R-6b3c-7 and R-6b3c-8); the first scope annotates the content shown. Without a scope flag, `--trust` answers only
the profiles' consent (ruling R-6b3c-7): the CLI never passes it to a scope's sync, so a terminal
asks that question, and a run that cannot ask (`CannotAsk`) refuses before any network contact when
a found project has entries you have not trusted (`refuseUnnamedTrust`, `untrustedModules`:
`checkParts` of its project.yml and active project profiles), hinting the run with the project
named. That check reads the project's trust before the update, and a project profile that extends
an updated remote profile changes what the project trusts, so an unscoped `--trust` run off a
terminal can write the profile and then have the project's sync refuse (residual, the safe way:
nothing is trusted unasked); the stop's `apply it later` hint applies it once reviewed, and naming
the project up front (`--project … --trust`) avoids it. Then each profile is handled on its own
(`updateRun`): `fetch` reads the kept profile (`profile.LoadRemote`), checks its recorded source
again (`remoteSpec`), fetches `profiles/` as
`--add` does into a run directory of its own and compares: an unchanged `Origin` is a notice;
otherwise `remoteContent` diffs the old entries against the new, with the whole content of each
changed one. `Consent` is asked once per changed profile. A No is that profile's outcome
(`ProfileOutcome`, `UpdateDeclined`) and the run goes on; a failure to fetch, read, check or write
one is its outcome (`UpdateFailed`, with the error and its hints) and the run goes on; an error of
`Consent` (Ctrl+C) stops the run, every later profile `UpdateSkipped`, and is returned as
`UpdateStoppedError` with the run so far. On a Yes, `write` gives up the shared lock, takes it
exclusively, sweeps, refuses when the kept profile is no longer the one shown, replaces it
(`writeRemote`), and the loop takes the shared lock again before the next question (R-6b3b-8);
failing to take it stops the run the same way, the profile written reported, and no scope is read
without the lock (the stop's hints are each scope's whole sync). A scope that cannot be read once
the profiles are written also stops the run, with every profile's outcome kept. The `ctx.Err()`
branch serves engine callers: the CLI installs no signal handler, so Ctrl+C outside a question ends
the process. Once every profile is handled, `updateScopes` has `scopeUse.after` read each scope
again with the profiles written (`uses` takes several) and keep the modules whose winning entry
changed (§19 P9), counting every other difference from `lock.json` as pending.
`ProfileUpdateResult.Scopes` lists the scopes in order with those modules (`UpdateScope.Apply`),
which the CLI syncs one after the other once the lock is released, stopping at the first that fails
or is declined as `sync --scope both` does; an unnarrowed run adds a notice that other checkouts
catch up at their next sync. Only a narrowed run reads the other scope a user sees from there (user
scope, or the project `foundProject` finds) and gives it a notice with the command that applies the
update there, or that it could not be read (R-6b3b-2, kept for that case); a narrowed scope that
does not use the profiles says that every scope that does picks the update up at its next sync only
when that other scope does not use them either. The update and its apply are two steps: the profile
is shared by every scope, and the next sync of any scope applies it, so a sync that stops leaves the
update kept and says so, with the `lola sync` of that scope and of each one not reached
(`updateNotApplied`; a conflict would stop that scope's sync again, so its hint is the sync with
`--force` and without the run's answers when `--force` overrides every conflict, and none when one
is another module's file; a No at the sync's question is an abort whose message names them, since an
abort prints no hints). A scope that cannot be read once the profiles are written refuses before
anything is applied, naming the sync of every scope. A refusal of a scope keeps its error's hints
once: the refusal is a new error, not a wrap, since `errs.Hints` walks the chain (`narrowUpdate`).
The CLI fails a run with a failed profile once the others are applied, or with that profile's own
error when the run named only one, and a stop (`UpdateStoppedError`) after a failure; every such
error is an `updateError`, whose `--json` error document is the run's document beside the wrapped
error's own fields. A run where every profile was declined, and Ctrl+C with no failure, are aborts.
`ProfileUpdateRequest.Flags` carries the run's `-y`, which every hint that repeats the run, or
syncs only the modules it moves, keeps; `Rerun` carries its `--trust=<kinds>`, `-n`, `--json`,
`--plain` and `--no-pager`, which only hints repeating the run keep (the CLI's sync hints keep the
`--trust` kinds that answer a sync). `Trust` covers the profiles' question when it has `profiles`:
each changed profile is shown through `Show`, and every one reaches one `Receipt` before any is
written. A hint naming profiles the run did not name keeps no answer (`-y`, `--trust` in any
form), and one naming a project an unscoped run did not name drops `--trust`.
A whole scope's sync hint carries neither (ruling R-6b3c-8).

**Removing one** (`Engine.RemoveProfile`, `internal/engine/remote_remove.go`) refuses without a way
to ask (`-y` answers; `--force` never does), under the shared lock taken first, as for an update,
then checks the run's scope, user scope and the project found from the working directory
(`foundProject`): through `uses`, the profile itself on, or an active profile or `default`
extending it, refuses unless `Force`, and so does a scope it cannot read (under `--force` only a
failure of the run's own `profileScope`, a tracked or unreadable `lock.json`, still refuses; a
scope whose layers cannot be read becomes a warning). No hint carries `-y` but the cannot-ask
refusal's, which asks for it (ruling R-6b3c-8): a `-y` answers the question it was given for;
the `--force` one keeps `-n`. After the question, under the
exclusive lock, it checks the profile is still there with the `source.json` it read before asking
(else `changed while you were asked`), and renames the directory aside (`.del-<name>-*`, which a
sweep only ever removes) before removing it, so the profile is gone in one step; a failure to
delete what was renamed aside is a warning, since the profile is gone and the next run's sweep
removes it. A name that is not a directory there is no profile. The modules it installed are left
to each scope's next sync, which reports the active profile as gone (§10).

**`extends:`** (Plan 6b §4.3) is resolved in `loadProfiles`, which lists the remote profiles and
reads each remote profile once per run, whether it is active, extended or both: a user or project
profile's layer holds the extended remote profiles' entries in list order, then its own, a later
entry replacing an earlier one of its name with no override notice (`Layer.over` records the
replaced remote profiles, which `desired` puts in `Wanted.Over`, so a profile's state and an
activation's `changes[].over` name them), and an entry without targets takes the extending
profile's. A name that is no remote profile refuses with the profile's file and line
(`Profile.ExtendsError`), reading as an invalid name does, so it never tells which remote profiles
exist (§19 S4). `Layer.via` keeps, per module, the remote profile an entry came from
(`trust.ExtendsView`: name, source, commit, digest), and `Wanted.via`/`syncEntry.via` carry it:
its pin is the remote profile's (`ownPin`, so `sync --update` refuses it and no pin is ever
written into the extending profile), and in a project profile's trust part the entry is recorded
with it (`trust.Entry.Extends`, `CoversEntry`), so a remote profile updated or replaced is a
change to every entry it gives, field `extends`, through the trust picker.

`Status` (`lola list`) reads the same layers: a row's `From` is the layer whose entry wins now,
else the one `lock.json` recorded (the manifest's base name when it recorded none); a profile's
entry that `lock.json` does not record is `not_installed` like a manifest entry. It returns the
layers' notices (missing profiles), and one counting the not-installed entries with the sync
that installs them (`notInstalled`, shared with `InstallCoaching`; the run's output flags kept),
and refuses a project whose `lock.json` git tracks
(`refuseTrackedLedger`, Plan 6b §19 S5), since a committed one could turn profiles on.

`Render` reads the layers too (Plan 6b §7): `renderCheck` judges each installed module by its
winning entry, so a profile's module is not "not in manifest" at project scope, and a module no
layer lists is left out naming the layer `lock.json` recorded (`profile:user:go no longer lists
it`). The user's own profile's entry skips the trust and folder-content checks, as at user
scope, except that one without targets of its own is left out when `project.yml`'s top-level
targets, which it installs to, are not the ones the trust record covers. A project profile's
entry is checked against its profile's part of the record, and its folder content as
`project.yml`'s is. Every unpinned git entry, whichever layer it comes from, renders `lock.json`'s
pin, so it is left out unless that commit is reachable from its ref (`source.CheckReachable`, as
sync checks a `ledgerPin`): an untracked `lock.json` planted in the checkout could name any
commit, for the user's own entry, trusted unpinned, and for a `project.yml` or project profile
entry that declined removals recorded unpinned alike. `renderPin` installs the winning entry's
pin, else `lock.json`'s version, and names where a failing pin lives: an edit of the user's own
profile, or `sync --update` for the manifest's or a project profile's. Render keeps or sets each
module's `from`, warns about missing profiles, and removes nothing for them.

`Profiles` and `ProfileState` (`internal/engine/profileview.go`) are what `lola profile` reads,
per scope. Both refuse a project whose `lock.json` git tracks before reading anything else
(§19 S5). `Profiles` reads the profiles directory's listing and `lock.json`, never a profile file
(§19 S8): each visible profile with its kind, path and 1-based position in the active list
(`default` is always active and has none), a missing-profile warning per stored name with no
file, the invalid-name count of each profiles directory as a warning, and a project's ignored
`default.yml`. `ProfileState` resolves a typed name (`resolveProfile`, §4.4: a bare name is, at
project scope, the project's profile or the user's, refused naming both qualified names and
files when both exist, with a hint per kind repeating the typed command with the name qualified
(D8), and at user scope the user's; `default` is `default.yml` at user scope only;
`user:go` and `project:go` name their kind; `remote:` names are unknown until 6b.3b, §19 P8; an
unknown name lists the profiles the scope can use), then builds
the scope's layers with the profile appended to the active list when it is off, as `--on`
would, and reports each of its modules' winning layer (`From`) and the layers that entry replaced
(`Overrides`, from `Wanted.Over`). The profile is read once, by `layers`. Over several scopes,
one the name does not resolve in is left out; when it resolves in none, the first refusal is
returned. `lola profile` (`internal/cli/cmd_profile.go`) resolves its scopes with
`ResolveReadScope`, as `list` does, and prints a table whose rows name their scope;
`ProfileNames` completes its argument from the profiles directory's listing alone. Its action flags' aliases (`--enable`, `--use`, …) are one flag each: `aliasFlags`
(`internal/cli/help.go`) records them in the flag's `lola_aliases` annotation and installs a
pflag normalize function mapping each alias to its canonical name, and the usage template renders
`.LocalFlags` through `aliasedFlagUsages` (`--on (--enable, --activate)`). With an
action flag, `profileAction` checks the one-action rule on canonical flag names after cobra has
parsed (§19 R7), and `profileApply` resolves one scope as every writing command does (the picker
on a terminal; `both` refused), then runs `syncScope` with the `ProfileChange`: the same trust
and removal questions as `lola sync`, `-y` and `--trust` as answers. The summary
(`printProfileAction`: the list's new state, each change, then `printSync`'s lines) goes through
`emit`, and so through `page`.

`Uninstall` refuses a module any profile layer lists (`refuseProfileModule`), even when the
manifest lists it too, since uninstalling would leave it wanted: each profile's hint is
`turn the profile off: lola profile <scope flag> <name> --off`, keeping the run's output flags,
and for `default`, which no command turns off, the error text names the edit of `default.yml`
and adds no hint. `InstallCoaching` counts the not-installed entries of every layer and names the
layers they come from;
`InstallAll` reads each scope's layers under its lock, before anything is fetched (a profile
that does not load refuses the install), and adds the precedence notice of each entry it writes
that differs from the highest profile's (`overridesProfiles`).

## Sync

`Engine.Sync` (`internal/engine/sync.go`) makes one scope match its manifest. The CLI adapts it:
it answers the two questions (`SyncRequest.Trust`, `SyncRequest.ConfirmRemovals`) through the
Prompter on a terminal (`Prompter.Trust`, the picker, and `Confirm`), `-y` confirming, and nil
(cannot ask) otherwise. `--trust`'s `entries` and `profiles` kinds (`Options.Trust`) answer the
changes of their kind without asking: the engine splits the report (`TrustReport.split`), shows
the covered part through `SyncRequest.Show` (the diff the CLI prints as a record) and asks `Trust`
only about the rest, merging the answer with `TrustAnswer.Kinds`.
`SyncRequest.Modules` limits the run to the modules named (`syncLimit`); `Update` re-pins the run's
git and archive entries. The steps, in order, under the scope lock (none in a dry run):

0. Refuse a project at the home directory and symlinked project state; then a tracked
   `lock.json` (`refuseTrackedLedger`), before the profiles it names are read (§19 S5).
1. Read the layers; refuse a scope without a manifest when no profile is in play: nothing a layer
   wants, no active profile, no module `lock.json` says a profile installed (an empty manifest
   means "remove everything"; otherwise the scope syncs as an empty manifest, §19 R4, §10);
   refuse a name that is neither wanted nor an installed module (`limitTo`); sweep day-old
   `run-*` directories (best effort, also in a dry run) and create this run's.
2. Validate every entry of the run, `desired(layers)` in name order limited to the names, before any
   network contact or prompt: `source.Parse` against `sourceRoot` and `source.Validate` with the
   entry's ref and path (git's `check-ref-format`); the pin's shape against the source's kind; the
   cache holding the name from another source or path (`refuseOtherSource`, through
   `Store.Origin` and `source.SameLocation`), coached with `lola cache rm`, except a local source
   the cache records that no longer exists (`Origin.LocalGone`), which the run's publish replaces
   (`syncEntry.goneFrom`, a warn notice); the effective targets. An entry a profile won keeps that
   profile in `syncEntry.from` (written to `lock.json`'s `from`) and its file, which errors name.
   A limited run notes the modules it left out of sync (`outOfSync`, from the layers and
   `lock.json` alone).
3. At project scope, `trust.Check` of the record against the manifest's view gives the status and
   the changes, limited to the run (`runReport`: the named entries' changes, and the top-level
   targets only when a named entry installs to them). Coverage (`Covers`) is the entry's own; a
   profile's entry is the user's own and always covered.
4. Containment (never for a profile's entry, which is the user's, §19 R5): a folder or local
   archive whose Source and Path the record does not hold, and any one whose source as written (a
   relative one joined to the project, compared through `paths.KeyID`) names a path inside the
   project (`lexicallyInside`: a pulled link can lead it out), must resolve inside the project's
   real root (`source.IsWithin`), else the run is refused before any diff, whatever `--trust` or
   `-y` say. Each covered manifest folder is then read (no network): content
   that is not the version `lock.json` records, or any content when it records none, marks the
   entry `folderChanged`, a trust change (`folderChanges`; an addition, `not installed yet`, when
   it records none: `notInstalled`). The removals are listed now
   (`syncRemovals`, from `lock.json` and the manifest; each path confined first, since `lock.json`
   is hostile input). Then the **early refusal**: a run that is not dry and needs an answer it
   cannot ask (the trust question with `Trust` nil, the removals with `ConfirmRemovals` nil)
   returns one `TrustRefusalError` naming every missing answer and cause (`syncRefusal`), with the
   field-only report (each entry to pin named with `pinReasons`, changed folders with their
   content) and the removals, before any fetch or `ls-remote`. Under `--json` the error document
   carries both (`JSONFields`, which `writeError` merges).
5. Resolve each entry: a folder is fetched every run; `--update` fetches the ref; an unpinned git
   or archive entry takes a snapshot of the cache head for its ref, or fetches the ref when there
   is none or the head has no commit (written before pins); a pinned entry takes a snapshot of
   its version (`Store.Pinned`), checking reachability for an uncovered git entry even on a hit,
   or fetches it by its commit (`fetchPin`). Every fetch is prepared in the run's staging
   (`Store.Prepare`) and must declare the entry's name. A covered entry at an unchanged pin (and
   not a changed folder) is committed to the cache now, without moving a head in a project dry
   run, and without replacing a gone source in one; everything else stays staged.
6. The report compares the record with the manifest **as it will be saved** (the pins the run
   writes applied), so a pin change is a trust change. `fillContent` (`content.go`) adds per added
   or changed entry the files against the version `lock.json` records (from the cache, never
   fetched; a lost or unreadable version lists every file as added with a note; the generated
   `lola.yml` only when it changed or went), the module MCP servers (`contentMCPServer`: argv and
   cwd quoted word by word as consent shows it, env, headers: the diff is their only consent),
   install hooks, and every runtime hook and agent MCP server the content declares
   (`declaredRuntimeHooks`: the module planned for every target lola knows without selections,
   probing no assistant), each MCP server and hook **marked** `added`, `changed` (a server's
   `previous` form attached), `unchanged` or `removed` against the version installed
   (`markServers`, `markHooks`; a hook is identified by all its fields). `note`, `reason`,
   `host_changed`, the folder lines (`folderEdits`) and `InstallsToTargets` complete it. Every value
   goes through `untrusted.Text`.
7. Plan every module (`planSync`) from the snapshot or staged tree, then ask. Dry: the report goes
   in the result. Otherwise `Trust` answers All or the entries accepted (`TrustAnswer`); one that
   accepts nothing is `ErrDeclined`. `accept` turns the answer into an `acceptance`: the accepted
   entries, whether the top-level targets are accepted (All, or an accepted added or changed entry
   without targets of its own; never a removed one), and the modules that **leave the run** (every
   change not accepted, and, when changed top-level targets are not accepted, every entry that
   installs to them). A partial answer drops those entries, their removals and their report
   changes, adds the `left as installed until you trust them` notice, and plans the rest again
   from the same staged trees under the same lock (`planSync` again, no fetch), so the summary is
   the apply. A declined entry's hooks never reach `prepare`.
8. Publish (`Store.Commit`, which checks the same-name rule again under the cache lock, and replaces
   a gone local source only after checking again that it is still missing): pinned fetches with
   `headIfAbsent`, the rest moving their ref's head; a declined entry's content is never
   committed. A dry run publishes nothing more, except `--update -n` at user scope.
9. Removals are confirmed through `ConfirmRemovals` before any hook consent is asked or recorded.
   A No (`declineRemovals`) applies nothing but saves `trust.Accept` of the loaded view for the
   answer given in step 7 (without the pins, which were not written), so the next sync asks
   again only the removals and the accepted entries whose pin or folder content it did not
   install; it returns a `RemovalsDeclinedError` (wrapping `ErrDeclined`) that says whether the
   record changed, names those entries (`Again`), says whether the record changes what
   `render` installs (`rendersOther`), and carries the run to repeat (`Retry`, `commandOf`: the
   sync, or an activation's `lola profile`).
10. `prepare` (runtime hook consent, the plan and its conflicts, install hook consent, the pending
    hook marks); a forced copy whose pending hook was declined again is kept by `keepUnhooked`
    (a dry run keeps it replaced, so its plan shows the hook); a dry run sets its closing commands
    (`ToApply`, `OffTerminal`, `EquivalentCommands`) and returns. `commit` applies the plan with
    the manifest edit of new pins staged in the same `plan.Apply` transaction (`saveManifest`). The trust record is written once, after the apply, and only
    when the user answered: `trust.Accept` of the loaded view for the accepted entries, through
    `saveManifest` when pins are written, else by `Trust.Save`, also when nothing was applied. A
    failed write fails closed: the error says what was applied and hints to run `lola sync` again
    (`syncAgain`, with the scope's flags; `install`, `uninstall` and `migrate` use it too).
11. `in_sync` is set when there is no step, pin, removal or module copy to replace and no
    active list an activation changes (one that moves no module still writes it); `Changed`
    names the modules the run changed. After a trust answer, `Equivalent` is the command that
    gives it off a terminal: the user's run with `--trust=` the kinds given and the report's for
    Trust all; for a partial answer, `lola sync <names> --trust=…` naming every accepted entry (a field-only change renders nothing
    but changes the record) and every changed module; `-y` when removals were confirmed. The
    CLI prints it after the removals are answered, when it asked the question.

**Activation** (`internal/engine/activate.go`, Plan 6b §6.3, §19 R3). `lola profile --on`,
`--off` and `--switch` are syncs: `SyncRequest.Profiles` (`ProfileChange{Action, Names}`) runs
under the same scope lock, after the tracked-`lock.json` refusal. `startActivation` lists the
profiles directory once, and `activeList` resolves each typed name (`resolveProfile`; only when
that finds none, `--off` also takes a stored name whose file is gone, as typed or as
`user:<name>` or `project:<name>`; a bare `--off` name that would match both, or that resolves to
a profile that is off while the other kind's is on with its file gone, is refused saying which
is on and which file is gone: both on and gone hints the typed `--off` per kind, one on and gone
hints turning it off and, when the other is the project's, turning that on in its place; a bare
`--off` name both kinds' files define, `resolveProfile`'s `ambiguousProfileError` carrying both,
is refused naming the one that is on with only its `--off` hint when one is, and is a `neither …
is on` notice when neither is, D8), refuses a name given twice, refuses `--off`/`--switch` of `default` (hint: rename
or delete `default.yml`) and any action on it at project scope, makes `--on default` a notice,
and builds the new list: `--on` appends in typed order (`by: on`), `--off` removes, `--switch`
replaces it in typed order (`by: switch`), at most 64. A list whose names do not change returns
at once with its notice (`already on (2nd of 3)`, `not on`, `already …`) and `in_sync`.
Otherwise the request's names become the qualified ones, so every hint repeating the run names
them as resolved. `loadProfiles` reads `default` and the union of the old and new lists once each,
keyed by qualified name, reusing that listing (a gone name warns only when the new list holds it);
`assemble` builds the old and the new layers from it. `layerChanges` diffs the new `desired`
against `lock.json` (`load`, `replace`, `unload`), and `moved` keeps what the change of list moves:
a module whose winning entry differs from the old layers' (absent, another layer, another entry),
and an installed module the new layers do not want that the old ones did or that `lock.json` says
a named profile installed. The other differences are pending from before: they are left to
`lola sync`, with the notice `N other changes are pending → lola sync <scope flag>`
(`outOfSync` is skipped). The run is limited to the moved modules (`syncLimit.only`, so none means
none), so trust (§8) and removals (§19 R15) see only them; precedence notices name only modules
the named profiles list (`desiredNoting`); the missing-manifest refusal does not apply. A trust
answer that leaves some entries as installed is refused: the new list is committed with the
apply, so an activation applies all it planned or nothing. `prepare`'s ledger gets the new list,
so it is written in the apply's commit, also when no module moves; a refusal, a No, an error or
an interrupt leaves the old list and files. The result embeds `*Activation`: `profiles` (the
list after the run), `changes` (`{module, action, from, over}`, `over` from `Wanted.Over`; an
unload's `from` is the layer `lock.json` recorded) and `Named`/`Files` (human output only). Hints
that repeat the run (`commandOf`) name `lola profile <names> --<action>` with the scope's flags
instead of `lola sync`.

**Prepare and commit.** `Store.Prepare` detects, normalizes, checks the cached copy reads the
same, and digests, in the run's staging, taking no lock and touching nothing in `modules/`.
`Store.Commit` copies the staged tree into a version (renamed into place, as before) under the
cache lock and sets the head; the run keeps rendering from its staged tree, the bytes that were
hashed. `Put`, used by install and `cache add`, is the two in a row, and lets a source the user
names replace a gone local one (`PutRequest.ReplaceGoneOrigin`). Content of an entry the user has
not trusted never enters the cache before they accept it, in a dry run included.

**Render.** `Render` checks every ledger module locally before anything is fetched
(`renderCheck`): no longer listed, another source than installed (or a folder whose cached source
is not the ledger's, at every scope), not covered or installing to uncovered top-level targets,
and a folder whose content does not load or is not the version installed (read into the run's
staging, never cached). The first that applies leaves the module as installed with a warn finding (codes
`not-in-manifest`, `source-differs`, `not-trusted`, `content-changed`) whose remedies `--strict`
lists instead of its generic advice (`Result.remedies`, `strictError`). The others render at the
manifest's pin, else the ledger's version (`renderPin`); a failed fetch under the pin is coached
as sync coaches it (`syncPinError`, `lola sync --update <m>`).

**The viewer and paging.** `emit` (`internal/cli/output.go`), help (`showHelp`) and the consent
diffs a question follows (`syncScope`, `profileAdd`, `profileUpdate`) all print through
`page` (`internal/cli/pager.go`, Plan 10 §2). `paging` decides, first match winning: `--json` or
`--plain`, stdout or stderr not a terminal (`WithTerminals` stands in for them in specs),
accessible mode (`app.accessible`), `--no-pager`, `NO_PAGER`, or `pager: false` in the user's `config.yaml` (default `true`, never a
project's, read only when the rules before it allow paging; unreadable, it is a warning and the
output is printed) print; a non-empty `$LOLA_PAGER` is the pager (`cat` alone prints); otherwise
the viewer. `$PAGER` is not read. `page` renders once with `style.paged`, which every capped
printer turns into an empty `more` (`style.more`), lifting the `filesShown` and `stepsShown` caps,
in the color of stdout. A pager runs as before (`runPager`): split on whitespace with no shell,
found with `exec.LookPath`, refused in the current directory (`exec.ErrDot`), fed on a pipe,
writing to stdout, with `LESS=FRX` added to `Engine.Environ` when `LESS` is unset and
`signal.Notify(os.Interrupt)` keeping Ctrl+C from killing lola. The viewer opens only when the
text, wrapped to the width, has at least as many lines as the terminal has rows (`app.height`:
`term.GetSize`, else `$LINES`); otherwise the text is printed. `viewerModel`
(`internal/cli/viewer.go`) is a bubbletea model over `bubbles/v2/viewport`: `ansi.Wrap` to the
width, again on `tea.WindowSizeMsg`; `g`/`G`/`Home`/`End` added to the viewport's keys; a footer
with the position in `style.strong` and the keys through `style.paint`; confirm mode, which answers
a `viewAnswer`: `y` apply, `c` change, `n`/`q`/Esc/Ctrl+C cancel (W4's install review). `app.view` runs it on the alternate screen, on stdout (read
mode) or stderr (confirm mode), with the command's stdin unless that is `os.Stdin` (then bubbletea
opens the terminal), and writes the text to the same stream after it quits. `page` returns only
when the pager or the viewer has exited, so the question comes after it. A missing, refused or
failing pager, or a viewer that cannot start, is a warning, and the capped text is printed.
`lola market <name>` fits every line to `app.width`: the terminal's width (`term.GetSize`), else
`$COLUMNS`, else no limit.
`printEntries` caps the name column at `maxNameCell` (32), printing a longer name whole on its own
line with the rest of its row indented under it (names are never cut: they are what you type),
cuts the version cell to `maxVersionCell` (16) columns and the source cell to `maxSourceCell` (40), or fewer when the row needs it, and the
description to what the row leaves, or puts it on its own line when that is under `minDescCell`
(20); `fitLines` wraps every other line at spaces (`wrapWords`), a field's value in its column,
and never breaks a word: one wider than the line is left whole on a line of its own. Widths are measured
and cut with `charmbracelet/x/ansi`.

**Accessible mode.** huh's line prompts read stdin in cooked mode, where Ctrl+C is a SIGINT.
While an accessible-mode form waits, `eofReader.catch` holds `signal.Notify(os.Interrupt)`; the
reader reads stdin in a goroutine, and an interrupt ends its input, so the form
returns `ErrAborted` (exit 0, the command's abort message). A goroutine left blocked on stdin
ends with the process, since a cancel always ends the command. lola installs no process-wide
handler.

## Marketplaces

`lola market` (Plan 6c §3.1) adapts `internal/engine/market.go`; the catalogs themselves are read
by the pure `market` package (see [Packages](#packages)), and every file and network access is
the engine's.

- **Storage** (§5). `markets.yaml` (`paths.Markets`, your file, read through `yamldoc.Read` and
  `market.ParseRegistry`, strict, following a symbolic link as `config.yaml` does, and edited by
  `market.AddRegistration`/`RemoveRegistration`, keeping comments) lists your marketplaces.
  Each fetched catalog is cached in `paths.MarketCache/<name>/`, `catalog.json` or `catalog.yml`
  byte for byte and `source.json` (`market.Pin`), written by `writeDir` (the remote profiles'
  crash-safe directory swap: a `.new-` directory renamed into place, the old one renamed aside
  first) and read by `loadMarket`, which refuses a link, a pin that is not lola's or does not name
  the registration's source, ref and path, and a catalog whose digest is not the pin's.
- **The markets lock** (`paths.MarketsLock`) is taken after the profiles lock and before every
  other: profiles → markets → selections → scope → cache → trust. `install` (with an address) and
  `sync --update` (with an entry that has `market`) hold it shared from after the profiles lock to
  the end of the run. `readMarkets` takes it shared
  (through `readSwept`, which, as for remote profiles, sweeps a killed writer's `.new-`, `.old-`
  and `.del-` directories under it exclusively first), and takes none for a read-only or dry run
  when no lola has made the lock file. Writers take it exclusively only to check the registration
  again and write.
- **`AddMarket`** (§6.2) checks the name and the typed source before any network contact
  (`marketSource`: a catalog-file URL by `market.IsCatalogURL`; otherwise `source.Parse` with the
  module rules, so a folder is made absolute with its links resolved; a local catalog file or
  archive is refused, coached to the folder that holds it; `source.Recorded.Check` and
  `source.Validate`) and a typed name that is already this marketplace (`sameMarket`). After the
  fetch, a name another marketplace has (typed or the catalog's) goes to `takenName`: `Rename` asks
  a free name on a terminal (`askName`, which asks again until the answer is a valid name no
  marketplace has), else the hint is `market.FreeName`'s `--add`; a catalog's own name that no
  marketplace can have is asked the same way, pre-filled with its free slug, else refused with
  the slug's `--add`. Those hints name the catalog file found with `--path` for a tree source, as
  another name would look for its own `<name>.yml`. A typed name that breaks the rule is refused
  with `module.ValidateName`'s account of what breaks it, and the `--add` of its lowercase form
  when that is a name. `noMarket` refuses a marketplace you do not have, naming `CloseMarkets` and
  hinting the caller's run with each (`ShowMarket`, `UpdateMarkets`, `RemoveMarket`, never `-y`). `fetchMarket` downloads a catalog file
  (`source.FetchFile`, 8 MiB, the source URL rules, and the archive download's redirect rules) or
  fetches the tree whole with `source.Fetch` into the run's staging, never through the cache, finds
  the catalog (`catalogIn`: `--path` as a file or a directory, else `market.CatalogFiles`;
  `realPath` refuses a link on the way), reads it with `fsx.ReadOwn`, parses it (`market.Parse`),
  records in `Pin.Absent` each relative entry whose directory the tree does not hold, and each
  path a content list selects that is not there as its kind needs, and binds it
  (`market.Bind`). Then, under the exclusive lock, with the name checked again, it writes the cache,
  then the registration, so a failed registration leaves a cache directory that listings, driven by
  `markets.yaml`, ignore, and the next `--add` replaces.
- **`UpdateMarkets`** checks every name before any network contact, then fetches each
  registration at its source, ref and recorded catalog file (`updateMarket`), replacing its cache
  under the exclusive lock; a failure is that marketplace's outcome and keeps its cache, and the
  CLI fails the run once the others are written. `marketChanges` compares the old and new models
  by entry name. `Markets` and `ShowMarket` only read; a cache that cannot be read lists as not
  fetched with a notice naming `--update`. Errors from the source, file and lock layers lose their
  prose hints on the way out (`unhinted`), as a marketplace's hints are commands only; their
  messages still say what failed.
- **`RemoveMarket`** asks (a nil `Confirm` refuses, hinting the same run with `-y`, which approves
  exactly that removal; no other hint adds `-y`) under the shared lock, then, under the exclusive
  one, writes `markets.yaml` without the marketplace and removes its cache, renamed aside to a
  `.del-` directory first.
- **Resolution** (§6.3). `resolveMarkets` resolves every `@<market>/<entry>` argument of an
  install, `cache add` (`AddEntry`) or `check` (`checkEntry`) into its `InstallRequest.market`
  (`marketPick`), under the shared markets lock, which an install takes after the profiles lock
  and before selections and scope; it reads the registration and the cached catalog, never the
  network, and refuses a marketplace you lack or have not fetched, an entry skipped or not listed
  (`market.CloseNames`), a bare `@<market>` and `--path`, before anything is fetched. `resolve` hands such a request to
  `resolveEntry`: the entry's `Spec` at the catalog's ref (or `--ref`, which drops the commit pin
  and the `market:` provenance), recorded with `market: {name, source}` from your registration
  and, for an entry whose catalog lists its skills, commands and agents, `components:`.
- **Selections** (user decision, 2026-10-22). `source.Components` is a catalog's selection of a
  relative entry's directory. `Spec.Components` makes `source.Fetch` fetch the whole directory
  (its own root, never a `module/` in it) and keep only those paths, each moved to
  `<kind>/<its last element>` with its execute bits (`selectComponents`); `Components.Check` refuses
  a path that is not clean and relative, two of one name, overlapping ones, or none. The selection
  is recorded wherever a fetch is rebuilt from: `manifest.Entry.Components` (`project.yml`,
  `user.yml`; never a profile, as for `market`), `ledger.Entry.Components` (`lock.json`, which
  `render`'s `recordedSpec` reads), `store.Origin.Components` (the cache, whose `Commit` refuses
  the name held with another selection, and which `cache update` and an install by cached name
  read), and `trust.Entry.Components` (omitempty, so earlier hashes stand; shown in the diff
  through `untrusted.Text`). So a sync, render or cache update of the entry stages the same module,
  with the same digest, without the marketplace. `sync --update` follows a catalog only while it
  selects the same components.
- **Pins** (§6.4). `fetchEntry` checks a commit pin with `source.CheckReachable` against the
  spec carrying the catalog's ref, never the commit (so `reach.go`'s `spec.Ref == commit`
  shortcut cannot apply, R-6c-12), before anything is fetched, then fetches by the commit and
  refuses another; `source.Spec.SHA256` and `FromCatalog` carry the archive sum and the redirect
  rule. `cacheEntry` prepares it under the entry's name (`PutRequest.Name` and `FallbackName`: a
  module that names itself otherwise is refused, one that does not takes the entry's name) and
  commits it with `headIfAbsent` when pinned. Every later fetch of an entry by its recorded name
  (`prepareFetch`, `fetchPin`, `fetchAs` for a cached name, `cache update`) falls back to that
  name too, so a teammate without the marketplace gets the same module and digest. The install
  shows each resolved address through `Options.ShowResolved` before any consent, and adds
  `entryNotices` (the `--ref` notice, the catalog's own notices) once and `dependencyNotices`
  (one install per dependency the same marketplace offers) per scope.
- **Browsing** (§6.5, §6.8). `MarketGroups` is every fetched catalog's installable entries
  (`MarketOffer`: the address, the source host, and whether the cache holds the name from the same
  source), read under the shared lock, never fetched; `InstallChoices` carries them as
  `Choices.Markets` for the module picker's groups, `MarketMeaning` is what a bare name may mean
  (only one your cache lacks and that is no source here),
  and `Search` (with markets, for `lola search`) appends the matching offers after the cache's
  hits, as `SearchHit.Where` `@<market>` with `Install`. The CLI's install flow turns a picker's
  answer into arguments (`expandMarkets`: `@<market>` on a terminal, a bare name your cache lacks
  that a catalog lists, all of a run's refused at once off a terminal), so the summary's
  equivalent commands name the addresses; a picked entry is
  fetched through `InstallChoices` like any argument, and its version (with the commit it was
  fetched at, `VersionRef.commit`) pins the install. `RemoveMarket` counts the entries of the
  known scopes whose `market` names it (`installedFrom`).
- **`@` in files** (§6.7, R-6c-7). `manifest.Address` recognises a `source` of the form
  `@<market>/<module>` (both parts valid names; anything else starting with `@` is a path, as
  before). `manifest.Load` and the profile loaders (`profile.Load`, `LoadProject`, `LoadRemote`,
  `Fetched`) check every other rule first, then report all such entries in one
  `*manifest.UnresolvedError` (the file, and each entry's name, address and source line), with no
  hints, since those depend on the reader. Every reader of a manifest or profile refuses it before
  its trust check and any network contact: `load` reads the manifest before the trust record, and
  `fetchCatalogs` (`sync --update`) reads it, and the profiles the run would read, before any
  catalog is fetched. `CoachUnresolved`,
  deferred in `InstallAll`, `Sync`, `Render`, `Uninstall`, `Status`, `AddProfile` and the update's
  `fetch`, and in `InstallChoices`, adds the hints from your `markets.yaml` (`unresolvedHint`): for
  an entry whose marketplace you have, `lola install @m/x` at the scope of the manifest it is in
  (matched by path among the run's scopes), with `-n` and the output flags, when the entry is
  named `x`; else `lola market <m>` with the output flags (a profile, or an entry named otherwise,
  which that install would not replace); none without the marketplace, the message then saying
  to edit the entry (`Unresolved.Coached` drops that text for an entry with a hint). An unreadable
  `markets.yaml` gives no hint. `ProfileState` (`lola profile <name>`), `UpdateProfile`,
  `RemoveProfile`, `market --remove`'s count (its warning, after `→`) and migrate's plan
  (`NewPlan`, with the run's output flags) coach the same way; an
  error a reader already coached is left as it is. An install replaces (§6.3): `replacingNames` are the entries
  its addresses install, and `manifestReplacing`, through `loadReplacing` in `InstallAll` and in
  the pickers' reads (and `InstallManifest` for the CLI's target lookup), reads a manifest whose
  unresolved entries include one of them as written (`UnresolvedError.Manifest`), returning the
  others, which `unresolvedNotices` warns of, each with its own hint. The entry is then written
  as any install's is (`planInstallModule`: a covered entry keeps its targets and rules), and
  `manifest.Edit` keeps every other unresolved entry as written; the trust refresh covers only
  what the install wrote. The pickers' uncovered-entry notice leaves such entries out. `readd` leaves such a
  refusal of a kept remote profile as it is, since adding it again would refuse again.
- **`sync --update`** (§6.6). `fetchCatalogs` reads the manifest once without the scope lock to
  name the marketplaces its entries of the run came through, and fetches each one whose
  registration has the recorded `market.source` (`sameMarketSource`) once (`updateMarket`, which
  writes the cache unless dry), under the markets lock held from after the profiles lock until the
  sync ends; a failed fetch is a warning. `validateSync` asks `follow` per entry with `market`: it
  follows the catalog's entry only at the same source, path and ref, giving the fetch the catalog's
  sum and redirect rule; any other case is a notice, with `lola install @m/x` when the catalog moved
  the entry's ref and none for a source, path or selection (the cache would refuse that install
  while it holds the name), and the entry updates as a plain one. In every sync, an entry whose `market` names
  another source than its own is fetched with `FromCatalog` (R-6c-13); `render` and `cache update`
  fetch from records without `market` and keep today's rule (residual). `resolveSync` then takes the catalog's commit pin
  through `followPin` (`fetchEntry`'s reachability check, a fetch by the commit, `headIfAbsent`).
- **`CoachMarketVerb`** (D8) refuses the Python lola's verbs, which the CLI routes to it whenever
  the first argument is one (`market.Verbs`); the translation is a hint only when it would run:
  a name you do not have gets the listing, one that cannot be a marketplace and a missing
  operand get none. The hidden `--enable` and `--disable` flags exist only for `set`.
- **Hints** are bare commands (`MarketRun`, quoted by `errs.ShellJoin`, every argument through
  `untrusted.Text`), carrying the run's output flags (`--json`, `--plain`, `--no-pager`, as lola
  profile's hints do; `Display`) and exactly the other flags of Plan 6c §11.3: `--ref`, `--path` and
  `-n` for the slug, collision and two-catalog `--add` hints, `-n` for the same-source `--update`
  hint, `--ref`, `--path`, `-n` and (for `rm` and `remove`, when typed) `-y` for the verb
  translations, `-y` for `--remove`'s own refusal off a terminal, none for the listing and
  `--update` hints. A refusal with no working command has no hint, and its message says what to do.

## Security model

- **Trust kinds cannot widen.** Each kind answers a fixed set of questions; bare `--trust` covers
  only `entries,profiles`, which run no code; a kind a command never asks is refused; a hint adds
  exactly the kinds its refusal misses and never writes `all`; `-y` and `--force` grant none. A
  grant is recorded only after the receipt's yes, so a No, a refusal or `-n` leaves no consent.
  **Residual risk:** a grant by `--trust` is recorded as a yes is, so on a machine whose
  `$XDG_STATE_HOME` persists (a CI runner), one job's `--trust=hooks` covers later jobs installing
  the same hook bytes; the digest keys bound it, and a fresh `XDG_STATE_HOME` per job removes it.
- **Hooks run code, with consent.** Install hooks run as the user, unconfined (sandboxing is on
  the roadmap), only after consent tied to the exact scripts. A changed script asks again; a
  changed file that a script runs does not, which the user docs say. Runtime hooks reach
  Claude Code's settings only under a consent tied to the rendered groups and the module
  version, so everything they could run from the module copy is covered; they go to the per-user
  settings file, never the shared one, and only when git cannot commit it.
- **Sources are not trusted.** Symlinks in module content are refused, including a file swapped
  for a symlink while it is copied; names that become paths or JSON keys are validated;
  `CopyTree` reads through `os.Root`; cache names and version ids are validated before use.
- **Destinations.** lola follows symlinked directories in the project, because dotfile managers
  use them, and shows writes that leave the project. **Residual risk:** a repository you check
  out can contain symlinks that redirect lola's writes elsewhere in your home directory.
  Conflict detection prevents overwriting content lola did not write, and the plan shows every
  redirected destination; review `lola install --dry-run` in repositories you do not trust.
- **Project state.** lola refuses a `.lola/`, `.lola/modules/` or `.lola/plugin-data/` that is
  a symlink, so a repository cannot point the module-copy swap, uninstall cleanup or plugin
  data outside the project, and it replaces a module copy that is not a real directory, so a
  committed link cannot point `${PLUGIN_ROOT}` and install hooks elsewhere.
- **Fetched sources.** Residual risks: lola honors the user's environment and git config
  (credential helpers, `insteadOf`, proxies) by design; git follows an initial http redirect
  itself, possibly to plain http; and a server can serve different content for the same ref.
  `cache update` and content digests make a change visible but do not prevent it; a version
  fetched again for an install must match its recorded digest, and a git commit must be on its
  ref. Checkouts convert nothing and run no filter program a repository's attributes name.
- **Remote profiles.** A remote profile and its entries reach `https://`/`ssh://` hosts by name
  only (R-6b3b-6/-9); lola never resolves names, so a name that resolves inside your network, and
  a redirect to an address inside it that git or the archive fetch follows, are still fetched
  (residual). A planted, untracked `lock.json` can change only the 'installed' annotation of
  `--add`/`--update`, never what is shown (R-6b3b-7).
- **Marketplace catalogs.** A catalog is a stranger's input, parsed by a package that cannot import
  `os` or `io/fs` (depguard), so no catalog string is looked up on disk: every source it names is
  classified from its text (R-6c-10), must be `https://` at a host by name (`source.CheckRemote`;
  ssh is skipped, as it would use your keys on a stranger's host, R-6c-18), and passes the
  recorded-source rules before anything else sees it. JSON keys are case-exact (R-6c-17), every read
  key and string must print as it is (R-6c-20), and size, nesting and entry counts are capped (size
  before decoding, nesting while it decodes, entries before any is read). No catalog field runs a
  command; entries that declare components, headers or a `headersHelper` are skipped (R-6c-4).
  Relative entries stay inside the fetched tree, which holds no links. `market.Entry.Spec` gives a
  source a catalog names (every entry but a relative one) `Spec.FromCatalog`, under which no
  redirect, of an archive download or of git, can take the fetch to a host the address rule refuses
  (R-6c-13), and an archive's listed `sha256`, checked before extraction (R-6c-6). Residual: host
  names are never resolved. **Known limit** (ruling of 2026-10-22): after install the stricter
  redirect rule follows an entry only through its `market:`; `lola sync` applies it to an entry
  whose `market` names another source than its own, but `render` (which fetches a pinned version
  from `lock.json`), `lola cache update` (from the cache's record), an install by cached name
  that fetches again (from the cache's record, when the cache has no head for the ref) and an
  entry installed with `--ref` (which records no `market:`) fetch under the rule of a source you
  typed. A server that
  later redirects such a source can lead those fetches to an internal host. `sync --update`
  fetches a catalog only for entries the trust record covered before the run.
- **Recorded sources.** `project.yml` and `lock.json` may be someone else's: their sources,
  refs, paths, commits and digests are checked when they are loaded, before any git run, for
  every command that reads them (`list` included). Names,
  paths, manifest metadata and runtime hook fields pass through `untrusted.Text` before lola
  prints them; other text a module contains (frontmatter keys quoted in finding texts, for
  example) is not yet sanitized.
- **Pulled manifests.** An entry of `project.yml` lola did not write (a pull, a hand edit) is
  never installed by name or rewritten: see [Trust record](#trust-record). `lola sync` shows every
  change with the content it brings and acts only on a yes or `--trust`, never `-y`; off a
  terminal it refuses before any network contact; uncovered local sources, and covered ones
  written inside the project, must stay inside it, checked before the diff; a covered folder
  whose content is not the version installed is a trust change; untrusted content stays in the
  run's staging until the yes, so a dry run or a No caches none of it. Residual risk: in an
  interactive or dry run the untrusted content is fetched (from a validated location) before the
  question, so the diff can show it. `render` still renders the versions `lock.json` records,
  which a pull cannot change because the ledger is private, and a tracked `lock.json` is refused
  by `sync`, `render`, `install` and `uninstall`. Step paths in `--json` output stay raw
  (JSON-escaped): `lola migrate` reads them as paths. So do `lola tidy`'s item `path` and
  `trash`, which scripts restore from; every other string it prints went through
  `untrusted.Text`, and MCP servers show only their server label (a program's base name or a
  URL's host), never arguments, environment, headers or a URL's other parts.
- **Tidy** moves only direct entries of declared destinations and direct members of declared
  server maps, never into a destination a link redirects onto the root, another destination,
  git's or lola's files or another project, never an instruction file, and never deletes: what it
  takes is in the trash. Residual: the rename into the trash is path-based (`os.Root.Rename`
  works within one root and `renameat` would need a new dependency), so a process running as you,
  or a git checkout, that swaps a path component between the re-check and the rename can redirect
  it; across file systems such a process can change an entry between the fingerprint and its
  removal.
- **Templates are untrusted input.** They render in the sandbox of [Templates](#templates): no
  file, environment, network or Go method is reachable, every value and the output are capped,
  and the output is checked and consented as a literal file with those bytes would be. The
  vendored liquid copy is outside `govulncheck`'s reach; its advisories are checked by hand
  ([release.md](release.md), item 3).
- **Unparseable files** are never rewritten, `selections.yaml` and `trust.json` included.
- **User files are untrusted.** `config.yaml` and `selections.yaml` are size-capped and strictly
  validated; their keys and values reach the terminal only through `untrusted.Text`.
- lola's own state files are `0600`; its XDG directories `0700`.

## Testing

Ginkgo specs run in-process on every `task check`, isolated from your home and assistants by
`testenv.Isolate()`; the end-to-end suites under `e2e/` run the release-built binary in a
hardened container (`task e2e`), before each release. [testing.md](testing.md) describes both:
the conventions, the git and archive harnesses, the runner, container and helpers, how to add a
suite or a case, and what is untested by design.

## lola migrate

`internal/migrate` adapts the Python lola's state to the engine; the CLI only renders its
`Result`. `Load` reads `installed.yml` and each named module's `.lola/source.yml` as untrusted
YAML (1 MiB, known keys with unknown ones as warnings, single-component names, no control or
format characters, aliases followed under a budget of 100000 names, counting list items, mapping
keys and records, since PyYAML writes them) and marks a `project_path` the design refuses as a
problem rather than an error. `NewPlan` groups records into destinations (Plan 5c §14), decides
each record's outcome, maps its source to what `lola install` takes, finds modules already
migrated by source in the destination's manifest, lists the paths each assistant recorded and what
is never migrated, and resolves each install's source once, through `InstallChoices`, pinning it
to that cached version (`InstallRequest.Version`). It verifies rather than emulates the Python
lola's module-root rules: a record listing a component the resolved version lacks is unmappable,
as is one whose source lola-go cannot fetch (hint: rerun once it is reachable), so lola-go never
reads `$LOLA_HOME/modules/<m>` to guess where the Python lola found the module. A module's records
at one destination migrate all or none, since an install would replace the Python lola's module
copy under a record left behind. The CLI computes the plan once and hands it to both the preview
and the confirmed run, so the run installs what the preview showed.

`Migrate` runs in phases so that nothing is written on a refusal or a cancel: a dry-run
`InstallAll` per destination (with the recorded paths that pass the guards as replaceable;
`coachClash` rewrites its refusal, naming each guarded recorded path a conflict lies at with its
guard, and each clash between two modules); the MCP entries left in place, across all
destinations, and the module copies and Python home they reference (each recorded module copy on
its own); the decisions per recorded path (owned → kept; guarded → kept, unless `--force` writes
it; written by the plan → replaced; else moved, or kept with `--keep-python`), refusing an install
that would swap out a `.lola/modules/<m>` neither the ledger nor a migrated record accounts for,
or that git tracks (read with `--literal-pathspecs`) or is a symbolic link, unless `--force` backs
it up; a consent pass, an `InstallAll` whose `BeforeWrite` stops it, so every hook question is
answered before anything is written; then, per destination, the real `InstallAll` with
`BeforeWrite` deciding again under the scope lock and copying into the trash the replaced paths
and every file a `--force` step writes over (where it really is, past the symbolic links on its
way; a directory copied in the same pass that holds it is its backup), the moves (each checked
against the ledger after the install), and the leftover files of replaced directories (unlinked
when they match the backup, read through `os.Root`, keeping a link lola-go writes through; listed
as kept instead with `--keep-python`). Then the Python lola's marketplaces are registered (below),
and the Python home moves last. The trash journals each entry
in `manifest.txt`, synced, before it copies or moves; it refuses to write through a symbolic link
inside it. `trash.Move` takes the entry's parent as an `os.Root` and its name: the parent's
path names the entry in the manifest and for the rename. Across file systems it copies through
that root (no link followed, no path resolved again), syncs the copy's directories, then removes
through the same root only the files and links whose fingerprint matches the copy
(`UnlinkIfBackedUp`'s rule) and the directories then empty; what remains was created or changed
after the copy and comes back as a `*trash.ChangedError` with the trash path. `CopyBytes` writes
given bytes, never reading the original, as a new 0600 file (Plan 6d §6.1).
Migrate reports such a partial move with the trash path of the part moved (the Python home's
`reason` says `moved in part`); that branch has no spec, as no fixture can time a change
between the copy and the removal ([testing.md](testing.md#untested-by-design)).

Marketplaces (Plan 6c §6.9): `Load` also reads `market/<name>.yml` (`name`, `url`, `enabled`,
with the same guards; a file whose name would not print as itself is skipped with a warning;
`cache/` is never read). A `url` is shown only through `shownURL` (`source.DisplayURL`: no user
info, password, query or fragment), and `translate` puts every source through the rule `--add`
applies (`source.Validate` for git, `source.CheckURL` for a catalog file), so one with
credentials or a query is `not-registered` with no hint; only `source.IsSCP` text is git's scp
form, so `ext::…` or `a:/b` is refused, never a folder under the working directory. Every name
the plan may register is claimed before any slug is chosen. `planMarkets`, in `NewPlan`, decides each without
any network contact, against your registrations (`engine.Markets`): `translate` maps the `url`
lexically, but for a `file://` URL or path, which it stats to tell a folder from the catalog file
in it (`--path`), on this machine and the user's own; a `url` lola cannot read is
`not-registered` with its reason and no hint; a name `market.ValidateName` refuses, or one of
yours has from another source, is `not-registered` with a `market.FreeName` slug's `--add`; a
disabled one with its `--add`; the same name, source and (when the url names one) path is
`already-registered`. The run's `registerMarkets` calls `engine.AddMarket` for each planned one,
after the destinations and before the home's decision, with `PythonName` for a tree whose url
names no file (`Market.lookup`): `fetchMarket` then finds the catalog with `pythonCatalog` (the
Python lola's `_pick_marketplace_yaml`: `<name>.yml`/`.yaml`, `marketplace.yml`/`.yaml`, else
the only root `.yml`/`.yaml` but `.pre-commit-config.yaml`) instead of `catalogIn`, and the
registration records that file. A failure is that marketplace's `failed` outcome (its hints, then
its `--add`, or for a lookup url the rerun of `lola migrate`, since an `--add` would not read the
Python lola's file; nothing for `ErrNoPythonCatalog`), never the run's. A skipped one whose hint
needs that file is fetched for it in the run (`engine.PythonCatalog`); a preview cannot tell, so
it says the run gives the command (`marketReport`). A marketplace not registered keeps the Python
home, as an unmappable record does, unless it is only disabled; a planned one makes the run
something to do (`nothing`).

Two engine additions serve it: `InstallRequest.Targets` adds targets to what a module's
manifest entry has, since modules at one destination were installed for different
assistants, keeping a module the manifest has at the version its ledger records (in
`InstallChoices` too, so the plan checks that version), and `InstallSpec.BeforeWrite` runs
after every refusal and prompt, before any write.

## lola tidy

The design is `docs/superpowers/specs/2026-10-22-plan-6d-tidy-design.md`. `Engine.Tidy`
(`internal/engine/tidy*.go`) moves what lola does not manage out of a scope's destinations into
the trash; the CLI only adapts it, and asks through the request's `Choose` and `Confirm`
callbacks (nil: lola cannot ask), as sync asks through `ConfirmRemovals`.

**Order.** Argument refusals (`--all` with names, a name also given to `--keep`); refused
symlinked `.lola/` state; the scope lock unless dry; the ledger check (`ledgerTracked`: git
tracking `.lola/lock.json`, or git unable to say, refuses, also with `--force`); `lock.json`;
keep.yaml with the run's `--keep` added and `--unkeep` removed (an `--unkeep` the section lacks
refuses); the targets (`-t`, else `project.yml`'s as far as the trust record covers them, as
`FixedTargets` does, else with a notice, plus every target the ledger records); the scan; the
selection; the review; the moves; keep.yaml's edits under `keep.lock`, taken after the scope
lock, so a run waiting on the scope lock reads the keep-list this run left; then the scope lock
released. A project whose destination resolves where a user-scope one does, or holds one or lies
inside one (a project at `$HOME`, a link between them), loads the user ledger too, under the user
lock taken after the project's; an item it owns is hinted with `lola uninstall --user`.

**Scan** (`tidyScanner`). Each target's `Destinations` is resolved with `fsx.Resolve`.
Destination-level locks skip a whole directory or MCP file with a notice: resolving onto the
root or above it, or outside it (`link_out`), first; then overlapping another destination still
in play, `<root>/.git` (and a gitfile's `gitdir:`), `<root>/.lola` or lola's own XDG directories,
each compared as written and resolved (`link_overlap`); a `.lola` directory found walking up
from it below the target's root, the root itself never counting (`other_project`, with a
`lola tidy -C` hint); an MCP file that is not a regular file, does not parse (`fsx.JSONMembers`)
or names a server twice. Units are the direct entries (`os.Lstat`, never followed) and the
direct server-map members. A unit is owned, and listed only for refusals, when a ledger artifact
lies at or below it, compared both resolved (a link unit's path goes through its parent only)
and logically below the root, so a link lola writes through is owned; an MCP member is owned when
an artifact records that file and pointer. Unit-level hard locks: reserved names,
instruction files at any depth (`filepath.WalkDir`, which never follows links), fifos, sockets
and devices, walk errors, paths `manifest.txt` cannot record, names no selector names exactly.
Git is asked per work tree (`source.WorkTree`, which runs no git) of each destination's resolved
directory, once, with `--literal-pathspecs ls-files -z`, over the links on the way to the
destinations (a tracked one skips its destination, `tracked_link`) and, without
`--include-tracked`, the units and MCP files; a unit is tracked when an output path equals its
path or lies below it. A git that is missing, fails or prints what lola cannot parse locks
everything it was asked about. Keep entries lock what they match last.

**Selection** (`tidySelect`). Names choose among candidates; a name matching none refuses with
each reason it matches (owned, tracked, kept, a hard lock) or "matches nothing", each with the
hint that lifts it, none for a hard lock. Without names or `--all`: an empty scan says "nothing
to tidy"; a dry run lists every candidate; `--keep`/`--unkeep` alone moves nothing; `--force`
refuses, hinting `--all --force`; a nil `Choose` (off a terminal, or `--json`) refuses, hinting
`--all -y`, saying it "cannot ask (off a terminal or with --json)", as a named run without `-y`
does (R-6d2-7); else `Choose` answers unit identities, refused before anything happens when one is
not a candidate, or a keep answer is not a candidate's escaped literal selector, and
`tidyEquivalent` keeps the equivalent command only when its selectors (with `-t` narrowed if
needed) select exactly the choice, with the keep answers as `--keep`; `Hidden` marks one that
exists but that `untrusted.Text` would alter. The review is skipped with `-y` or `--force`; a
nil `Confirm` refuses, hinting `-y`. Hints are built by `tidyRun`: the scope's flags
(`RootArgs`), names, then the run flags as given, and the output flags `--json`, `--plain` and
`--no-pager` as given (`TidyRequest.Display`, R-6d2-5). A dry run's closing hint adds `--all`
to a run without names only off a terminal or with `--force` (`TidyRequest.Interactive`); on
one, the run without names opens the picker.

**The picker** (`internal/cli/cmd_tidy.go`, `huh.go`). On a terminal the CLI sets `Choose` to
`tidyChoose`, which asks two `Prompter` steps over the scan: `TidyPick` offers every candidate
(option IDs index `TidyScan.Candidates`; labels are `tidyLabel`, already through
`untrusted.Text`, an MCP unit showing its key and a server label, never its value) with the
locked items and their reasons in the description, cut to the terminal height so that up to
three option rows stay visible (a note listing all of them in accessible mode, where huh prints
no description). `TidyKeep` offers one option per `TidyUnit.Literal`
selector of the candidates left, labelled with the selector and the assistants it covers, and
none for a selector one of whose candidates was ticked to move, since a keep entry keeps every
assistant's item of that name (R-6d2-6, R-6d2-8). No selector is named both to move and to
keep, and both layers enforce it: `tidyChoose` offers no keep option for a selector of an item
ticked to move, and `tidyEquivalent` keeps no command when a keep answer is a selector of a
moved item. Ticks map back to `UnitID`s and to selectors; ticking nothing in both is
`ErrAborted`. huh v2.0.3 toggles every option whose label equals the one under the cursor, so
`tidyOptions` keeps whole any labels the width would cut alike and numbers (` [n]`) labels still
equal in full. The engine then validates the answer (`offered`),
and `Confirm` prints the review with `Command`, "lola cannot show the equivalent command: it
holds characters lola does not print or a word too long to print" when `Hidden`, or "no single
command selects this set", only after the picker ran. `--force` and `--keep`/`--unkeep` without
names never reach `Choose`, so the picker never runs beside an `--unkeep` and a keep answer
cannot conflict with one (R-6d2-1 holds by construction, R-6d2-2).

**Apply** (`tidyScanner.apply`). Every destination is resolved again and checked again; each
chosen unit is `Lstat`ed through an `os.Root` of its destination, must be the same file
(`os.SameFile`, same type), and gets its unit locks and one git query again; then `trash.Move`
through that root, mirrored below the resolved scope root in the trash's `user` or project-hash
subdirectory. MCP files are read again; a member goes only when it still holds the value the scan
recorded; the re-read bytes go to the trash with `trash.CopyBytes` (0600) and the edit is
written with `fsx.WriteFileAtomic` to the resolved path. A unit that changed is skipped with a
notice; the first failed move stops the run, which reports what moved and still writes the keep
edits. Tidy writes no `lock.json`, manifest or trust record.
