# Layers

The stack of layers a scope's configuration is built from. It is split out of
[architecture](../architecture.md).

What a scope wants is a stack of **layers** (`internal/engine/profile.go`), lowest
first, the base then the active profile: the base is `baseLayer`
(`Layer.Base`): at user scope `default` (an empty layer when `profiles/default.yml` does not exist
yet), at project scope `project.yml`; `profileLayers.assemble(base, active)` puts the active profile
on it.

`default` is never a project layer. `Engine.loadStack` resolves the active profile (and an
activation's old one, in the same read) against `Engine.visible` (`profile.Visible`, and at project
scope `profile.Project`) and reads each profile once per run; a stored name with no
file is a warning naming `lola profile <scope flag> <name> --off` (built by `Engine.Command`, e.g.

`lola profile --user user:go --off`) and counts as off, and a profile that does not load refuses the
run. A profile's relative local sources are resolved against its directory before
anything else sees them: made absolute for the user's own profiles, so `lock.json` records them
absolute, and joined to `.lola/profiles` for a project's, so they stay relative to the root as
`project.yml`'s are.

`desired(layers)` walks the layers in order; a later entry replaces an earlier one of the same name
whole and, when they differ, adds a notice. Each `Wanted` carries its entry, `From` (the layer's
name: `project.yml`, `profile:user:go`, `profile:default`) and `Over` (the layers it replaced).

**The write target** (`internal/engine/target.go`).

`writeTarget(sc, layers, infos, req, verb, ask)` picks the file an edit writes, a `writeTo` (a
`Layer` plus `on`, whether the scope loads it, and `label`, how a write line names it):
`namedTarget` for `--profile` (`base`, or `default` at user scope, is the base; a profile that is
off becomes a layer through `offLayer`, which loads it by kind through `readProfile`, the dispatch
`loadStack` and `Context` share, and its write is saved, nothing applied, with a notice from
`switchRun`); else the profile on when it is yours or the project's; else, with a remote profile on,
the base once `Options.AskWhere` says so (refused without it, hinting `--profile base`); else the
base.

`TargetRequest` carries `--profile` and `--override` and the rerun that builds each refusal's
hints.

While a bundle is on (`Layer.Bundle` set on the profile layers, or `layerlessBundle` for a bundle
that gives none, decided from the ledger and the expansion so an empty or all-left-out bundle still
asks), `memberTarget` replaces rows 2 and 3: it asks `WhereQuestion` "which profile gets X?" among
the writable members, last first, even with one; neither `-y` nor `--force` answers it, and without
`AskWhere` it refuses with a `--profile` per member.

A bundle with no writable member asks whether to write the base. A `--profile` or `--to` naming a
bundle is `BundleTargetError`, hinting each member. An uninstall (`uninstallTarget`) removes from
the layer the module comes from and, for a base module, asks as it does under any profile.

**Where an inherited edit goes**. `placeEdit` decides it for an install of a module another
layer provides: `--override` answers without asking; else it asks, through `Options.AskWhere`, the
`WhereQuestion` "change it in that layer" (`WhereChoice{Profile: "base"}`) or "override it in the
target" (`WhereChoice{Override: true}`), offering Override alone for a read-only layer (a remote
profile, or one that is a member of the bundle on).

Neither `-y` nor `--force` answers it; `Engine.ask` without `AskWhere` returns the refusal with one
hint per choice (`flagRun`). A request equal to the entry the module already has is a notice and
writes nothing.

An override copies the
entry without its pin (`fileSource` also rewrites a local source as the target's file records it:
absolute for a profile of yours, relative to `.lola/profiles` for a project's), keeps the version
installed, and refuses a `project.yml` entry the trust record does not cover; an install that
changes only picks keeps the installed version too. A folder module, never pinned, installs the
version the run resolved, the cache's, which its component page offers (`planInstallModule`), and
its entry gets no pin.

**File edits.** A write to a profile file is a `fileEdit` (the `Layer` as read, and the
`manifest.Change`). `saveManifest` makes each through `Profile.Edit` and queues the result as a
`plan.ManifestWrite` whose `Check` is `Profile.Current` (a project profile's `Profile.Unchanged`),
so the file is checked against the bytes the run read just before it is written, in the same
transaction as `lock.json`. A file of yours (`default.yml`, your profiles) is written with
`KeepMode`, keeping the mode of the file it replaces (a dotfiles repository's `0644`); a new one,
and `project.yml` and the project's profiles, are `0600`.

After a successful project-scope apply,
the trust record is refreshed for what the write changed. `editsOf` turns each entry written into an
`Edit` (`Result.Edits`, JSON `edits`, the write line); `sync` and `update` report the re-pins they
write the same way. An install that writes a module a higher layer lists is save-only (`SaveOnly`):
the entry is written, nothing of it is installed while the other entry wins, and a warning notice
(`saveOnlyNotice`) says so when the entries differ.

**Context** (`internal/engine/context.go`). `Engine.Context(sc,
writes)` describes a scope for the context line and JSON `context`: the base (`default` or
`project.yml`), the active profile (from `lock.json` read keeping strays, resolved against the
profile listing, a user profile read in place of a remote one), and for `install` and
`uninstall` the file an edit writes.

It reads the active profile's file only to tell whether it loads, and never refuses: what it cannot
read is `Error`. The CLI calls it right after scope resolution (`printContext`,
`internal/cli/context.go`), which calls `addContext` once per scope.

`addContext` prints one scope's context per output mode: the line on stderr in human output, a
`--plain` row on stderr (`contextRow`: `context`, the scope, its root, the base, the profile on by
qualified name or `-`, and a bundle's members, comma-separated), and nothing under `--json`, where
`app.emit` splices `context` first into the document; `carryContext` wraps every command's refusal
in `contextError` so `writeError` adds it too.

A cross-scope move adds the source scope's context after the target's: `MoveRequest.Source` is
called with the other scope once `Move` knows the move crosses, before it asks or writes anything.

lola writes a profile of the user's own only on `install` or `uninstall`, and never a pin into it
(`pinsOf`, `Pins` and `needsPin` cover the
manifest's entries and project profiles' only (`syncEntry.own` is the rest).

`validateSync` gives a profile's unpinned git or archive entry the commit and digest `lock.json`
records for the same source, ref and path, whichever layer won before (`syncEntry.ledgerPin`; `from`
is not compared, since a planted `lock.json` sets it too), so it installs as a pinned entry does
until `--update` resolves its ref again; the version installed is what `planSync` records anyway.

At project scope
such a pin is checked like an untrusted entry's: its commit must be reachable from the ref even on a
cache hit, since an untracked `lock.json` planted in the checkout could name any commit.
`--update` naming a profile's pinned entry is refused with `edit the pin in <path>`; an unlimited
`--update` leaves such entries at their pins with an info notice. A pinned fetch's error names where
the pin lives (`pinnedIn`).

**`pins.yaml`** (`internal/config/pins.go`; `$XDG_CONFIG_HOME/lola/pins.yaml`)
is user scope's pin source for entries of your profiles, and it is synced with your config, so it is
read as untrusted input and written only by lola.
- **Source order.** For an own entry at user scope `validateSync` looks up its key (module,
  `source`, `ref`, `path`) in `pins.yaml` first, then in `lock.json` (`ledgerPin`), then, for an
  entry a cross-scope move brought, in the move's `seed` (the version the other scope installed). A
  remote profile's layer never reads `pins.yaml` for its modules. A pin that differs from what is
  installed is an ordinary change of the plan.
- **Reading.** `config.LoadPins` validates every module pin (`Pin.Check`: a host by name, a digest,
  no local source) and every `remotes:` record (`Remote.Check`, the host rules of a remote profile)
  with `path:line` refusals before anything is fetched. Local sources are never pinned.
- **Writing.** `commit` calls `pinsWrite` at user scope after the pre-install hooks, as the last
  `plan.ManifestWrite` of the apply (`Check`: the bytes `Sync` planned from, so a file changed in
  between refuses with "run the same command again"; `KeepMode`; the link followed, `linkTarget`).
  - `nextPins` sets the pins of the modules the run resolved or changed (`prepared.resolved` and its
    changes) from the ledger and keeps every other pin as read.
  - A pin of a module the run kept rather than chose is kept as read too: outside `Sync`, a module
    whose installed version equals the one the run read (`prepared.read`); in `Sync`, which planned
    from `pins.yaml`, a module whose version a move between scopes carried (`prepared.seeded`, from
    `syncEntry.seeded`: the seed wins over `pins.yaml`'s pin).
  - Either way a key `pins.yaml` has no pin for gets one; it prunes a pin no profile of yours lists,
    and records each remote profile on disk (`remoteRecords`).
  - The file is written only when its bytes change (`Pins.Same`), through `yamldoc`, which keeps the
    leading comment.
  - `pins.lock` (`paths.PinsLock`) is taken last, only around read, check and write, and released as
    soon as `plan.Apply` returns.
  - `writePinsNow` is the write of `--add`, `--update` and `--remove`, under the profiles lock: it
    touches `remotes:` only.
- **Lock order.** Selections lock, then each scope's lock in `lockOrder` (project roots by canonical
  key, then the user scope), with the profiles lock before the scopes' and `pins.lock` last.

**Project profiles** (`.lola/profiles/<name>.yml`) are committed, so their
layers (`Layer.Key`, the profile's name) are read with `profile.LoadProject` and treated as
`project.yml` is, not as the user's own: a sync's `syncEntry.key` marks their entries.

Coverage is the profile's part of the trust record (`rec.View.Part(key).Covers`);
`containLocalSources` checks their local sources against the project; a covered
folder's content is read and compared like a manifest folder's; an unpinned git or archive entry is
pinned, with the user's trust, and the pin written back into the profile, and `sync
--update` re-pins it.

The trust check (`trustParts`, `checkParts`) covers `project.yml` and every project profile of the
run's layers (the active ones and those being turned on), each with the entries of it that win only
(an entry that wins): an entry a later layer shadows is neither checked nor accepted, and
its record is left out of the check, so it is reviewed, with the content it installs, on whatever
run makes it win (a sync after `project.yml` drops the module, or an activation that turns a higher
profile off; the moved modules of an update include it).

Each change of a profile names it (`trust.Change.Profile`, `project:<name>`), and every other part
of the record is carried. `saveTrust` drops, on every save, the parts of profiles
whose file is no longer in `.lola/profiles`, never reporting them removed.

Any project-scope run with an active profile lists `.lola/profiles`, so a symbolic link there
refuses it even when only the user's own profiles are on. A trust answer is per module:
`acceptance.modules` holds the accepted entries per part, and `acceptance.record` accepts them part
by part. `saveManifest` takes the profiles' pins too (`profileEdit`): `Profile.Pin` edits the bytes
the run read, the apply writes them with `project.yml` and `lock.json`, and the record is refreshed
per part.

`Sync` checks a project profile's winning entry against its part as it checks a manifest entry.
`SyncNames` (completion) leaves project profiles out of the layers, since it runs no git and a
committed `lock.json` could name them; `InstallCoaching` refuses a tracked `lock.json` before it
reads them.

A project profile with the name of an active `user:` profile replaces it only through an
activation that names it: `--on project:go` or `--switch` naming it
makes it the profile on in `user:go`'s place (`replaces` lets `--on` do so without the switch
question).

`replaced` finds that pair between the old and the new profile on, and `replacingOf`
adds it to the activation's trust report (`TrustReport.Replacements`, with the modules it
changes); a pending replacement forces the trust question, which for an activation is all or none.
`lola sync` never swaps them: `syncLayers` keeps the profile on and adds
`replacementNotices` (`project:go also exists → lola profile project:go --on`), as activations
and the listing do. Nothing about a declined replacement is stored.

**Remote profiles** (`$XDG_DATA_HOME/lola/profiles/<name>/`) are listed by
`Engine.visible` beside the local ones (`profile.Remote`) and read with `profile.LoadRemote`. Their
layers are the user's own (`Layer.Key` empty, so `own()`): the consent was given when the profile
was fetched, so no project's trust view holds them; every entry is pinned, so nothing is
written back, and `ownPin` coaches a pin problem with `lola profile remote:<name> --update`.

Local beats remote (decision 4): `resolveProfile` looks for a bare name among the local profiles
first, refuses a name that local profiles of two or more kinds define (`ambiguousProfileError`,
every candidate named), and takes the remote profile only when no local one has its name;
`shadowing` names the local profile that shadows a remote one, which `activeProfile` refuses to turn
on and listings report (`ProfileRow.ShadowedBy`).

An active `remote:<name>` whose name a user profile now has is read as that profile (`userShadow`),
with the notice `user:go now replaces remote:go`, on every run: the stored name is never rewritten,
so the remote profile is used again once the user's file goes, and `--on user:go` leaves the profile
on as it is (`user:go is already on, in place of remote:go`).

A project profile is never swapped in by a sync: the `replaced`/`replacements` pairs take an active
`remote:` profile as they take a `user:` one, and `--on project:go` takes the active namesake's
place. `validateSync` refuses a remote profile's entry that still parses as local (a Windows drive
path, the one local form a remote source check can miss).

**The profiles lock** (`$XDG_STATE_HOME/lola/locks/profiles.lock`) is the first
lock any run takes.

`readProfiles` takes it shared for every run that reads the remote profiles (`Sync`, `Install`,
`Uninstall`, `lola list` (`Status`), `lola profile`'s listing and state (`Profiles`,
`ProfileState`), the install coaching and completion (`InstallCoaching`, `SyncNames`), and
`--add`/`--update`/`--remove` while they fetch, show and ask), and lists them under it: when a
killed writer left a directory behind (a `.new-`, `.old-` or `.del-` directory; any other name
starting with `.` is not lola's and is left alone), it lets go, takes the lock exclusively, finishes
that work (`sweepRemote`) and starts again, so a crash between an update's two renames never makes a
profile that is on look gone.

A dry run takes it too, but
never sweeps, and takes none when no lola has made the lock file yet. The writers take it
exclusively (`lockProfiles(true)`) only to check again that what they showed still holds and to
write, so no question is ever asked under it and syncs elsewhere go on.

**Adding a remote profile** (`Engine.AddProfile`, `internal/engine/remote_add.go`)
asks no scope: `ProfileAddRequest.Scope` is the scope a flag named, nil for none,
and only picks the scope the content is compared with; nil takes the first of `knownScopes`, the
project `foundProject` finds, else user scope, the read-only commands' rule; a `.lola/` in the home
directory is never that project (`IsHome`), and is skipped with a notice.

Hints that repeat the run are built by `ProfileRun`, which carries the scope flag only when one was
given, and the run's own flags (`ProfileAddRequest.Rerun`; a hint that sets `-n` or `--trust` itself
keeps the others, and `refuseKept`'s `--update` hint never `--trust`, which answered the add's
content); a found project that cannot be read refuses with the run at user scope as
one more hint.

It checks everything it can before any network contact: the name (`remote:go` taken as `go`, another
kind refused, then `remoteName`: valid, not `default`), the source (`remoteSpec`:
`source.CheckRemote`, which takes only `https://`, `ssh://` and git's scp form at a host named by
name; then `source.Parse`, which never looks a URL or scp form up on disk;
`source.Recorded.Check` and `source.Validate`), that the scope is readable and its
`lock.json` untracked (`profileScope`), and that it can ask (a nil `Consent` refuses, naming `-n`
and `--trust`).

Under the shared lock it fetches only `<--path>/profiles` of the source into the run's staging
(`fetchProfiles`: `source.Fetch`, which refuses a link in it or on the way to it; a
missing `--path` or `profiles/` is told apart through `source.MissingPathError.Exists`).

`profile.Fetched` reads each named (or picked, `pickRemote`, which never offers `default`) profile;
`remoteContent` diffs its entries against none (`trust.Check`) and, for each, resolves its module at
its pin into the same staging (`remoteEntry`: parsed without the disk, refused when local,
`source.Validate`, `checkPinShape`, `resolveSync`) and lists its whole content, every file, MCP
server and hook as added, whatever the scope's `lock.json` says, which only annotates it
(`FilesNote`); nothing is published to the cache.

After `Consent`, under the exclusive lock, it sweeps, checks no profile of the name appeared
(`refuseKept`, which also refuses a file in its place), and `writeRemote` writes `profile.yml` (the
bytes fetched) and `source.json` into a `.new-<name>-*` directory under `RemoteProfiles`, flushes
that directory, and renames it into place, renaming any previous directory aside to `.old-<name>-*`
first and removing it after, and syncing the directory; `sweepRemote` puts an
`.old-` directory back when nothing took its place and removes the rest (`.new-`, and `.del-`, a
removal's).

Once the rename succeeded the profile counts as written: a later failure (flushing, removing the
`.old-` one, or `visible` checking for a local namesake) is a warning notice; the add or update
still succeeds. The run's staging is removed on every path.

**Updating them** (`Engine.UpdateProfile`, `internal/engine/remote_update.go`) takes the profiles
lock shared first (`readProfiles`, whose sweep finishes an interrupted write, so a profile left
aside is found), then resolves the profiles (`updateNames`):
`ProfileUpdateRequest.All` lists every remote profile (`profile.Remote`), none being a notice;
otherwise each of `Names` is resolved to a kept remote profile (`remoteNamed`; a bare name is the
remote one, since only those are updated), a name given twice taken once.

The CLI refuses names with `--all`, neither, and a name given twice, since its hints keep the flags
of the run (`runWithout`), but for one naming profiles you did not name, which drops `--trust` and
`-y`; `--all` is in the table of flags per action, so other actions refuse it.

It refuses before it contacts the network when it cannot ask, and an engine call naming neither
profiles nor `All`.

It asks no scope: `ProfileUpdateRequest.Scopes` are the scopes the flags named,
none for no flag; none applies in every scope of `knownScopes` (the project `foundProject` finds,
never the home directory, then user scope), one in that scope only (a project at the home directory,
`IsHome`, is refused before the profiles lock is taken, hinting the run at user scope), and two
(`--scope both`, which the CLI takes only when the project has `.lola/`) in both, the project first.
`ProfileRun` repeats such a run with `--scope both` (and `-C <root>` for a project not found from
here).

Each of those must pass `profileScope`, and `scopeBefore` must read what each wants (`uses`), before
any network contact: a scope it cannot read refuses there, so nothing is kept that cannot be
applied, with `narrowUpdate` hinting the run narrowed to each other scope without `-y` or `--trust`,
which were given for the run as a whole (without a scope flag, the message names a
found project's untrusted changes); the first scope annotates the
content shown.

Without a scope flag, `--trust` answers only the profiles' consent: the CLI never
passes it to a scope's sync, so a terminal asks that question, and a run that cannot ask
(`CannotAsk`) refuses before any network contact when a found project has entries you have not
trusted (`refuseUnnamedTrust`, `untrustedModules`: `checkParts` of its project.yml and active
project profiles), hinting the run with the project named.

That check reads the project's trust before the update, and a project bundle that lists an updated
remote profile as a member changes what the project trusts, so a `--trust` run without a scope flag,
off a terminal, can write the profile and then have the project's sync refuse (residual, the safe
way: nothing is trusted unasked); the stop's `apply it later` hint applies it once reviewed, and
naming the project up front (`--project … --trust`) avoids it.

Then each profile is handled on its own (`updateRun`): `fetch` reads the kept profile
(`profile.LoadRemote`), checks its recorded source again (`remoteSpec`), fetches `profiles/` as
`--add` does into a run directory of its own and compares: an unchanged `Origin` is a notice;
otherwise `remoteContent` diffs the old entries against the new, with the whole content of each
changed one. `Consent` is asked once per changed profile.

A No is that profile's outcome (`ProfileOutcome`, `UpdateDeclined`) and the run goes on; a failure
to fetch, read, check or write one is its outcome (`UpdateFailed`, with the error and its hints) and
the run goes on; an error of `Consent` (Ctrl+C) stops the run, every later profile `UpdateSkipped`,
and is returned as `UpdateStoppedError` with the run so far.

On a Yes, `write` gives up the shared lock, takes it exclusively, sweeps, refuses when the kept
profile is no longer the one shown, replaces it (`writeRemote`), and the loop takes the shared lock
again before the next question; failing to take it stops the run the same way, the
profile written reported, and every scope is read under the lock (the stop's hints are each scope's
whole sync).

A scope that cannot be read once the profiles are written also stops the run, with every profile's
outcome kept. The `ctx.Err()` branch is the CLI's Ctrl+C or SIGTERM outside a question, which
cancels the run's context (`cli.NotifyContext`). Once every profile is handled, `updateScopes` has
`scopeUse.after` read each scope again with the profiles written (`uses` takes several) and keep the
modules whose winning entry changed, counting every other difference from
`lock.json` as pending.

`ProfileUpdateResult.Scopes` lists the scopes in order with those modules (`UpdateScope.Apply`),
which the CLI syncs one after the other once the lock is released, stopping at the first that fails
or is declined as `sync --scope both` does; an unnarrowed run adds a notice that other checkouts
catch up at their next sync.

Only a narrowed run reads the other scope a user sees from there (user scope, or the project
`foundProject` finds) and gives it a notice with the command that applies the update there, or that
it could not be read; a narrowed scope that does not use the profiles
says that every scope that does picks the update up at its next sync only when that other scope does
not use them either.

The update and its apply are two steps: the profile is shared by every scope, and the next sync of
any scope applies it, so a sync that stops leaves the update kept and says so, with the `lola sync`
of that scope and of each one not reached (`updateNotApplied`; a conflict would stop that scope's
sync again, so its hint is the sync with `--force` and without the run's answers when `--force`
overrides every conflict, and none when one is another module's file; a No at the sync's question is
an abort whose message names them, since an abort prints no hints).

A scope that cannot be read once the profiles are written refuses before anything is applied, naming
the sync of every scope. A refusal of a scope keeps its error's hints once: the refusal is a new
error, not a wrap, since `errs.Hints` walks the chain (`narrowUpdate`).

The CLI fails a run with a failed profile once the others are applied, or with that profile's own
error when the run named only one, and a stop (`UpdateStoppedError`) after a failure; every such
error is an `updateError`, whose `--json` error document is the run's document beside the wrapped
error's own fields.

A run where every profile was declined, and Ctrl+C with no failure, are aborts.
`ProfileUpdateRequest.Flags` carries the run's `-y`, which every hint that repeats the run, or syncs
only the modules it moves, keeps; `Rerun` carries its `--trust=<kinds>`, `-n`, `--json`, `--plain`
and `--no-pager`, which only hints repeating the run keep (the CLI's sync hints keep the `--trust`
kinds that answer a sync).

`Trust` covers the profiles' question when it has `profiles`: each changed profile is shown through
`Show`, and every one reaches one `Receipt` before any is written. A hint naming profiles the run
did not name keeps no answer (`-y`, `--trust` in any form), and one naming a project an unscoped run
did not name drops `--trust`. A whole scope's sync hint carries neither.

**Removing one** (`Engine.RemoveProfile`, `internal/engine/remote_remove.go`) refuses without a way
to ask (`-y` answers; `--force` never does), under the shared lock taken first, as for an update,
then checks the run's scope, user scope and the project found from the working directory
(`foundProject`): through `uses`, the profile itself on, or a bundle on that has it as a member
(`Layer.listed` names the bundle files for the hint), refuses unless `Force`, and so does a scope it
cannot read (under `--force` only a failure of the run's own `profileScope`, a tracked or unreadable
`lock.json`, still refuses; a scope whose layers cannot be read becomes a warning).

No hint carries `-y` but the cannot-ask refusal's, which asks for it: a `-y`
answers the question it was given for; the `--force` one keeps `-n`. After the question, under the
exclusive lock, it checks the profile is still there with the `source.json` it read before asking
(else `changed while you were asked`), and renames the directory aside (`.del-<name>-*`, which a
sweep only ever removes) before removing it, so the profile is gone in one step; a failure to delete
what was renamed aside is a warning, since the profile is gone and the next run's sweep removes it.

A name that is not a directory there is no profile. The modules it installed are left to each
scope's next sync, which reports the active profile as gone.

**Removing a profile of yours** (`Engine.RemoveProfile` routes to `ownToRemove`, then `removeOwn`,
`internal/engine/profile_remove.go`) resolves the name first: `user:<name>`, or a bare name only a
profile of yours has; a bare name both a profile of yours and a remote one have, a project's
profile, the base and an unknown name refuse.

`refuseInUse` is shared with the remote path: it refuses a profile that is on, in the bundle on or
in a bundle the one on nests (`uses` counts bundles, so a nested or empty one is found), before `-y`
is offered, so no hint names a removal that is then refused, and `removeOwn` runs it again under the
exclusive profiles lock.

The file
moves to the trash (a link as a link) after the question; one that changed while you were asked is
kept. `pins.yaml` is not written: the next user-scope apply drops the pins no file lists.
`namingBundles` adds a notice per bundle of yours that still names the profile, with the take-out
run (`unlistRuns` / `unbundleRun`).

**Editing a profile** (`lola profile [<name>] --edit`) is `lola install` of
the modules into a named profile. `EditArgs` is the one place a name and modules are told apart (the
first name is the profile when a profile has it and no module does, `base`, `default`, a qualified
name or `--new`; a name that is both is asked in `bothRead`).

`EditTarget` builds the `TargetRequest` (`Edit`, `New`) that `installRun` words as the install it
is, so the questions and hints name `lola profile … --edit` (`placeEdit`'s flag forms are `base` and
`--override`); `madeTarget` is the write target of `--new`, and `MakeProfile` makes a profile alone.
`--override` and a copy out of a project file pass the one `carriedOut` guard (the move's trust
guard and `refuseInProject`), as `lola install --override` does. A profile of yours on in several
scopes is computed once from its file and synced in each scope where it is on (`EditScopes`).

The bundle edit is `EditBundle`: `profile.SetBundle` rewrites the file, `profileWrites` puts it and
any bare member it makes in the apply's transaction with `lock.json`, and `reaches` walks the
expansion's `seen` to find where the bundle is on or nested. A project bundle's pulled member list
(`pendingMembers`) is asked about, shown with the run's own sync changes, before the edit records
it; `-y` and `--force` never answer it.

A bundle that does not load because of a member that resolves to nothing in another bundle is passed
over with a warning naming its take-out (`Engine.recovering`), so `--off` and `--edit --bundle`
always work; an edit that newly reaches such a bundle judges it in full. The CLI's `editTarget`
makes the request and `Rerun` the hints; the module page keeps the unticked modules in
`installFlags.keep`, and `dropped`, `askDrop` and `removeDropped` ask once and remove what was
unticked, in the run's scope and then in each other scope the profile is on.

**Writing a profile file.** `saveManifest` writes the data files of a profile through
`linkTarget` with `KeepMode`, so a file that is a link keeps its link and a file keeps its mode (a
split used to replace the link and write 0600). A run that only writes profile files sets
`syncLimit.files` and applies nothing, so a pulled change of the bundle on is the next applying
run's question.

**Bundles** are expanded in `loadStack` by `expansion`
(`internal/engine/bundle.go`), once per active profile: a profile that lists modules is one layer; a
`bundle:` file is replaced, depth-first and in order, by its members' layers over the base.
`expansion.member` resolves a member by the kind of the file that lists it (yours: bare is yours,
else remote; a project's: bare is the project's, else remote, never yours; a `project:` member of
yours works only inside that project), and `expand` reads each file once (`load`, `remote`).

A profile or bundle reached a second time keeps its first place (`seen`; one notice per name,
`noted`), so a pulled project's diamond of bundles costs the files and edges, not the paths. The
limits are `maxNesting` (8 levels) and two separate counters, `placed` (profiles) and `bundles`,
each capped at `activeprofile.Max` (64); a cycle (closing member's `path:line` and the chain), a
depth or a cap refuses with no hint, the file and line saying what to edit.

Each placed layer carries `Layer.Bundle` (the bundle on it comes through), `chain` (the `bundleRef`s
it comes through: qualified name, trust key for a project bundle, member list as written), and
`listed` (every bundle file that lists it, for `--remove` hints). A later layer's entry replaces an
earlier one whole, with the notice `profile over overrides profile go-review in bundle dup`
(`Wanted.Over` names the replaced layers).

A remote member this machine lacks is `leaveOut` (a warning, once, in its place in the member order)
and `needed` (restore.go) restores it only when every project bundle on its chain has a member list
the trust record covers (`rec.View.Part(key).Bundle` equals the bundle's members): pulled content
fetches nothing before trust. A project bundle whose members are all left out gives no layer, so
`expansion.project` records every project bundle it expanded and `Layer.expanded` carries them on
the base layer, to be asked about in `trustParts`.

Trust of a project bundle's members: `trust.View.Bundle` holds the member list in the bundle's own
part, `Report.Bundle` carries its change (Field `bundle`, through `ProfileTargets`, so it is
asked as a profile's change; order matters), and `accept` leaves the entries an untrusted bundle
brings as installed.

A remote member's entries carry `Layer.member`/`Wanted.member`
(`trust.MemberView`: name, source, commit, digest) and, in the part of the project profile that
lists them, `trust.Entry.Member` (field `member`), so an updated remote profile is a change to
each entry it gives; its pin is the remote profile's (`ownPin`), so `sync --update` refuses it
and no pin is ever written into a bundle. `trust.json` from before 12.3, whose entries have
`extends`, is refused by `beforeBundles` with the records to remove: there is no legacy read.

The retired `extends:` key is refused by `profile.extendsGone` (a typed `*ExtendsError`, hinted
again by the engine's `coachExtends`, which knows the remote profiles and so picks a free name for
`<name>-modules`) with the edit that keeps what it did; `lock.json` naming `user:base` is refused by
`ledger.ReservedError`, and a `base.yml` is listed as reserved, never as a profile.

**Making and splitting profiles** (`internal/engine/new.go`, `split.go`). `lola profile <name>
--switch --new` and `--split` write profile files in the same transaction as the sync that
applies them. `profileWrites` collects the files a run makes (`fileEdit` with `data`, and a `check`
that the path is still missing or unchanged), what to read in place of the disk, the `profile.Info`
of each (so a listing sees them before they exist) and, per project file, the trust the run takes as
already given.

`Engine.creating` holds it for the run: `readFile` reads a made file from it, `visible` lists it,
and the sync's commit writes the files with `lock.json` and re-checks each (`check`), so a file made
or changed while a question was open is never overwritten.

`newProfiles` builds one run's files: the profile (empty, `--from` copied by `copyOf` with
`sameMembers` rewriting a bundle's members so they name the same profiles in the new file, or a
bundle) and each bare `--bundle` member that does not exist, made empty (`newMembers`); a `project:`
member must already exist.

A copy from a project profile into yours goes through `outOfProject`, which shares the move's checks
(`localInProject`, `installedAsTrusted`) rather than repeating them. What a project write pre-trusts
is only what lola wrote on the user's command or what the record already covered where it copied
from. `madeFile` parses the bytes the way the loader would, so a file lola would later refuse is
never written.

`Split` (split.go) reads the mixed file with `profile.ReadMixed` (which refuses any shape the loader
refuses for another reason, so no entry is dropped) and `Mixed.Split` (the `modules:` and `targets:`
key nodes move with their comments), picks a name no visible profile of any kind and no `pins.yaml`
remote has (`free`, 60 characters at most), writes both files after one check of both, and records
trust only for what the old part covered (`CoversEntry`; targets only if unchanged).

The bundle's member list is not recorded, so a project's split is asked about as any bundle change
when the split bundle is on: `-y` alone then gets a `TrustRefusalError`; a split of an off profile
writes and asks nothing, and the list is asked about at switch-on.

When the profile is on, directly or as a member (`splitOn`, decided through the expansion),
the transaction is the scope's sync. Any read of a mixed file is a `*profile.MixedError`;
`mixedError` turns it into a `SplitOfferError` hinting the `--split`, and the CLI's `offerSplit`
(`internal/cli/profile_split.go`), wrapped round commands by `offerSplits`, shows the plan and asks
`Split <name> now?` on a terminal, then runs the command again; its own questions are wired through
`syncQuestions`, so the command's `-y` never answers the split's trust.

`Status` (`lola list`) reads the same layers: a row's `From` is the layer whose entry wins now,
else the one `lock.json` recorded (the manifest's base name when it recorded none); a profile's
entry that `lock.json` does not record is `not_installed` like a manifest entry.

It returns the
layers' notices (missing profiles), and one counting the not-installed entries with the sync
that installs them (`notInstalled`, shared with `InstallCoaching`; the run's output flags kept),
and refuses a project whose `lock.json` git tracks
(`refuseTrackedLedger`), since a committed one could turn profiles on.
The CLI's `printStatus` shows a row's `From` through `fromLabel` (`internal/cli/cmd_core.go`),
which names the layer as the context line does, from the run's contexts (`default`, `project.yml`,
`profile go-review`, `docs (bundle work)`); `--plain` and `--json` keep `From` as recorded.

`Sync` reads the layers too, and so does `lola update`, which is `Sync` with
`SyncRequest.Installed`: the entries it judges are the layers' winning entries (`Wanted`), so a
profile's module is not "not in manifest" at project scope. Every unpinned git entry, whichever
layer it comes from, takes `lock.json`'s pin and must be reachable from its ref
(`source.CheckReachable`), since an untracked `lock.json` planted in the checkout could name any
commit.

A project profile's entry is checked against its profile's part of the trust record and
its folder content as `project.yml`'s is. An entry from a remote profile is left as installed
under `update`, with a notice to run `lola profile <name> --update`, because the pin lives in a
file lola does not write.

`Profiles` and `ProfileState` (`internal/engine/profileview.go`) are what `lola profile` reads, per
scope. Both refuse a project whose `lock.json` git tracks before reading anything else. `Profiles`
reads the profiles directory's listing, `lock.json` and each profile file of yours and the
project's, to tell a bundle (`profileRow`; a remote profile never bundles, so its file is not read):
each visible profile with its kind, path, `State` (`base` for `default`, `on`, `member` when
`onState` finds it in the expansion of the bundle on, else `off`), `Bundle` and the `Members` of a
bundle as `bundleMembers` orders them.

A file that does not load, or a bundle that does not expand, is a warning and its row stays listed:
a listing is never refused by a profile it does not need. It also gives a
missing-profile warning per stored name with no file, the invalid-name count of each profiles
directory as a warning, and a project's ignored `default.yml`.

`ProfileState` resolves a typed name (`resolveProfile`: a bare name is, at project scope, the
project's profile or the user's, refused naming both qualified names and files when both exist, with
a hint per kind repeating the typed command with the name qualified, and at user scope the
user's; `default` is `default.yml` at user scope only; `user:go`, `project:go` and `remote:go` name
their kind, and a bare name no local profile has names the remote one; an unknown name lists the
profiles the scope can use).

It then builds the scope's layers with the profile in place of the active one when it
is off, as `--switch` would, and reports each of its modules' winning layer (`From`) and the layers
that entry replaced (`Overrides`, from `Wanted.Over`).

The profile is read once, by `layers`. Over several scopes, one the name does not resolve in is left
out; when it resolves in none, the first refusal is returned.

`lola profile` (`internal/cli/cmd_profile.go`) resolves its scopes with `ResolveReadScope`, as
`list` does, and prints a table whose rows name their scope; `ProfileNames` completes its argument
from the profiles directory's listing alone. Its action flags' aliases (`--enable`, `--use`, …) are
one flag each: `aliasFlags` (`internal/cli/help.go`) records them in the flag's `lola_aliases`
annotation and installs a pflag normalize function mapping each alias to its canonical name, and the
usage template renders `.LocalFlags` through `aliasedFlagUsages` (`--on (--enable, --activate)`).

With an action flag, `profileAction` checks the one-action rule on canonical flag names after cobra
has parsed, and `profileApply` resolves one scope as every writing command does
(the picker on a terminal; `both` refused), then runs `syncScope` with the `ProfileChange`: the same
trust and removal questions as `lola sync`, `-y` and `--trust` as answers. The summary
(`printProfileAction`: the profile the run turned on or off, each change, `printSync`'s lines, then
`now:` and the context line of the profile on after the run) goes through `emit`, and so through
`page`.

`Uninstall` refuses a module any profile layer lists (`refuseProfileModule`), even when the manifest
lists it too, since uninstalling would leave it wanted: each profile's hint is `turn the profile
off: lola profile <scope flag> <name> --off`, keeping the run's output flags, and for `default`,
which no command turns off, the error text says to remove it from `default.yml` and adds no hint.

`InstallCoaching` counts the not-installed entries of every layer and names the layers they come
from; `InstallAll` reads each scope's layers under its lock, before anything is fetched (a profile
that does not load refuses the install), and adds the precedence notice of each entry it writes that
differs from the highest profile's (`overridesProfiles`).
