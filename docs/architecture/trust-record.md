# Trust record

Maintainer notes on Trust record. It is split out of [architecture](../architecture.md).

`project.yml` is committed and someone else's after a pull, so lola keeps its own account of what it
wrote there.

The **view** of a manifest is what can change what lola fetches or writes:

- The top-level `targets` and each entry's `name`, `source`, `ref`, `path`, `commit`, `digest`,
  `targets`, `rules`, `member` (set only on a project profile's entry that comes from a remote
  profile that is a member of a project bundle; see [Layers](#layers))
- The member list of each project bundle (`View.Bundle`, in its own part)
- `market` (shown in the diff as `market (as written)`, through `untrusted.Text`, never checked)
- `components` and `vars` (what its templates render with; shown as `name = value` pairs through
  `untrusted.Text`, compared by their JSON, so a list `trust.json` decodes as `[]any` equals the
  entry's)
- Modules sorted by name, empty values omitted.

`EntryOf`, `equal`, `clone`, `take` and `fieldChanges` each list the fields by hand. `trust.json`
(`$XDG_STATE_HOME/lola/`) maps each project key (`paths.ProjectKey`, compared with `SameKey`) to its
view and the hex sha256 of the view's canonical JSON. An entry is **covered** when the record holds
an entry of its name with every view field equal.

Top-level `targets` are recorded; `lola sync` shows a change to them (`trust.Check`), an install
refuses to take uncovered ones unasked (`untrustedTargets`, set per scope in `InstallAll` when no
`-t` is given; the pickers get them as `ScopeChoices.UntrustedTargets` and ask instead), and `Sync`
(so `update`) asks about a module that installs to them. A project with no record covers nothing:
there is no seeding from `lock.json` (user decision, 2026-10-13: lola-go is unreleased, so no older
project needs it). User scope has no record; `default.yml` and the user's other profiles are the
user's own, and the engine never loads one for it.

A project's profiles (`.lola/profiles/<name>.yml`) are committed too. The view
gains `profiles`, a map from each profile's name to its **part**: its entries, sorted by name,
each with the targets it installs to (its own, else the profile's), so a change to a profile's
`targets` is a change to every entry it gives them to. `profiles` is omitted when empty, so a view
without profiles hashes as it would without the field.

`View.Part(key)` is one file's part (`""`: `project.yml`'s
targets and entries) and `WithPart` puts one back; `Check` compares a view with the record's
`project.yml` part only, and a profile is checked against a record holding its part.
`Accept` and `Refresh` take the key of the part they change and carry every other part as it is, so
trusting one profile's entries never touches `project.yml`'s record or another profile's.

- **Reading.**
  - The version is decoded on its own first, as `manifest.Load` reads `project.yml`'s: a newer one
    is refused with the hint `upgrade lola` (not for a key the newer lola added) and a negative
    one as unreadable.
  - Then strict JSON with unknown keys and trailing data refused, at most 4 MiB, and each
    record's hash checked against its view.
  - A file that fails is an error with a hint and is never overwritten.
  - The file and `$XDG_STATE_HOME/lola` are refused when a symbolic link or (outside Windows,
    whose permission bits mean nothing) group- or world-writable; the opened file must be the one
    checked (`os.SameFile`).
  - Go reports a Windows junction as irregular, so a state directory that is one is refused as
    not a directory.
  - A version up to this lola's is read, 0 included, as `consent.json` reads its own.
- **Writing.** Under `$XDG_STATE_HOME/lola/locks/trust.lock`, taken after every scope lock and
  held only around the read-modify-write: the file is read again and only this project's record
  replaced, then written `0600` through `fsx.WriteFileAtomic`. The state directory, `locks/` and
  the lock file get the read checks before the lock file is created, and a save that would make
  the file larger than the 4 MiB a read accepts is refused, which leaves the file as it was.
- **Refresh.** lola writes every manifest through `engine.saveManifest(sc, loaded, change,
  profiles, rec, applied, hint, apply)`.
  - It edits the manifest (unless `change` changes nothing), writes the pins of `profiles` into
    those project profiles, and hands the edits to `apply` (the commit, which stages them with the
    ledger).
  - After a successful apply at project scope it saves `trust.Refresh(record, "", view(loaded),
    view(loaded.Applied(change)))`, and refreshes each profile's part the same way.
  - Every field the write changed takes its new value, an added entry is recorded whole, a
    removed one is dropped, and every other field keeps its trust state: a write never covers a
    field it left alone, and a field changed in an entry the record does not hold is not
    recorded.
  - `loaded` is a clone of the manifest read under the scope lock.
  - A failed save leaves lola's own edits uncovered (fail closed). The error says what the caller
    applied (`applied`; an uninstall says it removed what was asked), and which scopes of the
    same install it then did not reach, with the caller's `hint`.
  - An apply that writes no manifest or profile (uninstalling some targets of a module
    `project.yml` does not list) saves no record: it has nothing new to cover.
- **Accept.** A sync records what the user accepted with `trust.Accept(record, "", view(loaded),
  modules, targets)`:
  - the entries named take their view in the loaded manifest (an entry the view lacks is
    dropped);
  - the top-level targets take theirs when `targets` is set;
  - every other entry keeps its record.
  - Accepting every change and the targets returns the view itself, which is what an
    all-or-nothing answer records; a partial answer, or a run limited to some modules, names
    fewer.
  - With pins to write, `saveManifest` refreshes `&Record{View: Accept(…)}`, so only the accepted
    entries' pin fields move.
- **Refusals.** Over an uncovered entry, these are refused (`notTrusted`, wrapping
  `ErrNotTrusted`) with the hint `review and apply it: lola sync <m>`:
  - an install by name (in `InstallAll` and `InstallChoices`, checked for every argument at every
    scope before any is resolved, so nothing is fetched);
  - an install that adds targets (`lola migrate`'s);
  - an uninstall that keeps some of the module's targets.
  - The message, from `untrustedWhy`, says "changed for it since you last trusted it", "added it
    since you last trusted the file" for an entry the record never held, or "has not been trusted
    on this machine" for a project without a record.
  - No hint suggests installing from the source, which would re-pin to what the source served
    and skip sync's review.
  - An install by source writes the entry as new: it inherits no `targets`, `rules`, `ref` or
    `path`, and `planInstallModule` drops the entry from `loaded`, so the refresh records it
    whole. With no targets given, the module keeps the targets `lock.json` installed it on (the
    user's own installs chose them; a ledger git tracks is refused, see below), so what is
    installed is what the entry records.
  - An argument that names an uncovered entry is refused as a name even when `source.Parse` would
    take it as a folder: `resolve` takes a cached name first and applies the entry's ref to any
    name-shaped argument, and the pickers' fetch caches the folder under that name, so only
    `./<name>` is unambiguous.
  - Removing a module is always allowed.
  - The pickers never offer what the install would refuse after the summary: an explicit name is
    refused at listing, a cached module with an uncovered entry is left out under one notice per
    project scope (`uncoveredEntries`), and `moduleScope` shows a source over an uncovered entry
    as the new entry it becomes.
  - Add a source… caches the source and lists it by name, so an uncovered entry stays out of the
    pickers (only a source argument writes its entry as new and trusted), and Change scope onto a
    scope where a kept pick is uncovered is the refusal, not a dropped pick.
  - `lola migrate` reports the module as unmappable with the refusal's reason and hint; it refuses
    after `resolve` has fetched the source, which is the user's own legacy configuration.
- **Tracked ledger.** At project scope every operation that reads the ledger runs
  `refuseTrackedLedger` after loading the scope and before resolving or fetching anything.
  - The operations: `InstallAll`, `InstallChoices` (so the pickers refuse what the
    install would), `Uninstall`, `Sync`, `Status` (`lola list`), `InstallCoaching`, and
    `profileScope` (what `lola profile` and the remote profile commands read, which refuses
    before reading anything else).
  - It runs `git ls-files --error-unmatch -- .lola/lock.json` through `GitCommand`
    (`gitTracked`).
  - Tracked is refused (hint `git rm --cached .lola/lock.json`, as `git -C <root> rm --cached …`
    unless run from the root, and none when `untrusted.Text` would change the root).
  - A git failure in a work tree is refused (fail closed, with a hint to make `git status` work
    there; `dubious ownership` is the common cause).
  - Git missing from `PATH` is a warning notice (a picker warning in `InstallChoices`).
  - This is what makes the ledger this checkout's own, which the targets of an install by source
    above and every removal rely on.
- **Canonical locations.** `source.Parse` resolves a folder or local archive with
  `filepath.EvalSymlinks(filepath.Clean(p))`, once (an archive link whose target has a different
  archive suffix is refused, since the suffix picks the extractor); `install` records a relative
  source against the project root with its links resolved (`canonicalRoot`), and every reader of a
  recorded relative source (`sourceRoot`: sync's validation)
  resolves it against that root too, so it resolves however the root was reached. The cache's
  same-source checks accept a location an earlier lola recorded through a link
  (`source.SameLocation`).
- **Hints.** Every `lola sync` command lola prints, in a hint, a notice, a dry run's closing lines
  or the picker's equivalent command, comes from `Engine.SyncCommand` (built on `Command`, which
  every other scoped command in a hint uses too).
  - It is `lola sync`, then the scope's flag (`RootArgs`): `--user` at user scope, `--project`
    when `FindProject` of the working directory (`Engine.Getwd`, `os.Getwd` unless a spec sets
    it) is the scope's root (`FoundHere`, which install's equivalent commands use to decide
    `-C`), else `-C <root>`; a command without a scope flag would ask for its scope on a terminal.
  - Then the names and flags, each through `untrusted.Text` and `errs.ShellJoin`.
  - A hint that repeats the user's run carries its names, `--update` and `--force`,
    `--trust=` the kinds given, and `-y` as given (`SyncRequest.Yes`, set only for a run at the
    one scope the user named: `syncFlags.alone`) unless the hint adds `-n`
    (`SyncRequest.rerun`; the shadowed-profile hint, which names another
    profile, drops it); the "… and N more" of a capped file list or
    dry-run plan names the user's run with `-n --json` (`Engine.ListAll`).
  - An install's `retry with: lola install <names>` (`withRetry`) is built per scope with
    `Command`, and left off a refusal for want of trust (`ErrNotTrusted`, the untrusted top-level
    targets) or for conflicts (`plan.ConflictError`), whose own hints are the way forward.
  - A conflict refusal that `--force` overrides hints `Options.ForceRun`
    (`plan.Request.ForceRun`): the CLI's `forceRun`, the user's run as typed with `--force`
    added.
  - One it cannot override hints nothing from the plan; `ownedConflicts` (in `prepare`) says what
    frees each destination another module owns (`Conflict.Owner`): the owner's `lola uninstall`
    at the scope, keeping `Options.Display`, hinted only when the install's command line named
    the claimant (`Options.named`); else, in the reason, removing the claimant from the layer
    that lists it (its `ledger.Entry.From`).
  - The other reasons (one module providing a destination twice, two of one run) say what to do
    in the message.
  - When a run names no scope and none is found or configured, the refusal hints
    `ScopeRequest.Run`, the run without its scope flags (`scopeFlags`), plus each scope flag the
    command takes.
  - install's no-assistant refusals hint the run plus `-t <id>`, and a project at the home
    directory `InstallSpec.UserRun`.
  - Every such rerun keeps `-t` and `-n` and drops `consentFlags` (`-y` and
    `--trust`), but `completeRun`, which drops only `--trust`:
    - the missing-scope refusal's `ScopeRequest.Run` and install's `-t <id>` hints complete the run
      with what a terminal would have asked, so its `-y` answers the completed run's question.
    - `runAs` drops `-y` from a dry run's hint, which asks nothing, and on another command (`lola
      info`'s next step is an install) keeps only the flags that command takes, and never `-y`.
    - `runWithout` repeats a bool only as given (`--x=false` against a true default, nothing for a
      false one).
  - `confirm`'s refusal off a terminal hints the run with `-y`.
  - `lola migrate` needs no case of its own: its process's directory decides, not the `Cwd` it
    passes.
