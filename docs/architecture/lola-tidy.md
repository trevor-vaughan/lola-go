# lola tidy

Maintainer notes on lola tidy. It is split out of [architecture](../architecture.md).

`Engine.Tidy`
(`internal/engine/tidy*.go`) moves what lola does not manage out of a scope's destinations into
the trash; the CLI only adapts it, and asks through the request's `Choose` and `Confirm`
callbacks (nil: lola cannot ask), as sync asks through `ConfirmRemovals`.

**Order.** Argument refusals (`--all` with names, a name also given to `--keep`); refused symlinked
`.lola/` state; the scope lock unless dry; the ledger check (`gitTracked`: git tracking
`.lola/lock.json`, or git unable to say, refuses, also with `--force`); `lock.json`; keep.yaml with
the run's `--keep` added and `--unkeep` removed (an `--unkeep` the section lacks refuses); the
targets (`-t`, else `project.yml`'s as far as the trust record covers them, as `FixedTargets` does,
else with a notice, plus every target the ledger records); the scan; the selection; the review; the
moves; keep.yaml's edits under `keep.lock`, taken after the scope lock, so a run waiting on the
scope lock reads the keep-list this run left; then the scope lock released.

A project whose destination resolves where a user-scope one does, or holds one or lies
inside one (a project at `$HOME`, a link between them), loads the user ledger too, under the user
lock taken after the project's; an item it owns is hinted with `lola uninstall --user`.

**Scan** (`tidyScanner`). Each target's `Destinations` is resolved with `fsx.Resolve`.
Destination-level locks skip a whole directory or MCP file with a notice: resolving onto the root or
above it, or outside it (`link_out`), first; then overlapping another destination still in play,
`<root>/.git` (and a gitfile's `gitdir:`), `<root>/.lola` or lola's own XDG directories, each
compared as written and resolved (`link_overlap`); a `.lola` directory found walking up from it
below the target's root, the root itself never counting (`other_project`, with a `lola tidy -C`
hint); an MCP file that is not a regular file, does not parse (`fsx.JSONMembers`) or names a server
twice.

Units are the direct entries (`os.Lstat`, never followed) and the direct members of a server map.

A unit is owned, and listed only for refusals, when a ledger artifact lies at or below it, compared
both resolved (a link unit's path goes through its parent only) and logically below the root, so a
link lola writes through is owned; an MCP member is owned when an artifact records that file and
pointer.

Unit-level hard locks: reserved names, instruction files at any depth (`filepath.WalkDir`, which
never follows links), fifos, sockets and devices, walk errors, paths `manifest.txt` cannot record,
names no selector names exactly.

Git is asked per work tree (`source.WorkTree`, which runs no git) of each destination's resolved
directory, once, with `--literal-pathspecs ls-files -z`, over the links on the way to the
destinations (a tracked one skips its destination, `tracked_link`) and, without `--include-tracked`,
the units and MCP files; a unit is tracked when an output path equals its path or lies below it. A
git that is missing, fails or prints what lola cannot parse locks everything it was asked about.
Keep entries lock what they match last.

**Selection** (`tidySelect`). Names choose among candidates; a name matching none refuses with each
reason it matches (owned, tracked, kept, a hard lock) or "matches nothing", each with the hint that
lifts it, none for a hard lock.

Without names or `--all`: an empty scan says "nothing to tidy"; a dry run lists every candidate;
`--keep`/`--unkeep` alone moves nothing; `--force` refuses, hinting `--all --force`; a nil `Choose`
(off a terminal, or `--json`) refuses, hinting `--all -y`, saying it "cannot ask (off a terminal or
with --json)", as a named run without `-y` does; else `Choose` answers unit identities,
refused before anything happens when one is not a candidate, or a keep answer is not a candidate's
escaped literal selector, and `tidyEquivalent` keeps the equivalent command only when its selectors
(with `-t` narrowed if needed) select exactly the choice, with the keep answers as `--keep`;
`Hidden` marks one that exists but that `untrusted.Text` would alter.

The review is skipped with `-y` or `--force`; a nil `Confirm` refuses, hinting `-y`. Hints are built
by `tidyRun`: the scope's flags (`RootArgs`), names, then the run flags as given, `-n` for a dry run
(the closing hint that applies it clears `Dry`), and the output flags
`--json`, `--plain` and `--no-pager` as given (`TidyRequest.Display`); a dry-run hint
carries no `-y`, the closing hint that applies a dry run carries it only as given, and
`TidyOneScope`'s hints drop it, given for both scopes.

A dry run's closing hint adds `--all` to a run without names only off a terminal or with `--force`
(`TidyRequest.Interactive`); on one, the run without names opens the picker.

**The picker** (`internal/cli/cmd_tidy.go`, `huh.go`). On a terminal the CLI sets `Choose` to
`tidyChoose`, which asks two `Prompter` steps over the scan: `TidyPick` offers every candidate
(option IDs index `TidyScan.Candidates`; labels are `tidyLabel`, already through `untrusted.Text`,
an MCP unit showing its key and a server label, never its value) with the locked items and their
reasons in the description, cut to the terminal height so that up to three option rows stay visible
(a note listing all of them in accessible mode, where huh prints no description).

`TidyKeep` offers one option per `TidyUnit.Literal` selector of the candidates left, labelled with
the selector and the assistants it covers, and none for a selector one of whose candidates was
ticked to move, since a keep entry keeps every assistant's item of that name.

No selector is named both to move and to keep, and both layers enforce it: `tidyChoose` offers no
keep option for a selector of an item ticked to move, and `tidyEquivalent` keeps no command when a
keep answer is a selector of a moved item.

Ticks map back to `UnitID`s and to selectors. When nothing is ticked to move, the keep step needs a
pick (`TidyKeepStep.Required`), so the choice is never empty; cancelling either step is
`ErrAborted`. huh v2.0.3 toggles every option whose label equals the one under the cursor, so
`tidyOptions` keeps whole any labels the width would cut alike and numbers (` [n]`) labels still
equal in full.

The engine then validates the answer (`offered`), and `Confirm` prints the review with `Command`,
"lola cannot show the equivalent command: it holds characters lola does not print or a word too long
to print" when `Hidden`, or "no single command selects this set", only after the picker ran.
`--force` and `--keep`/`--unkeep` without names never reach `Choose`, so the picker never runs
beside an `--unkeep` and a keep answer cannot conflict with one.

**Apply** (`tidyScanner.apply`). Every destination is resolved again and checked again; each
chosen unit is `Lstat`ed through an `os.Root` of its destination, must be the same file
(`os.SameFile`, same type), and gets its unit locks and one git query again; then `trash.Move`
through that root, mirrored below the resolved scope root in the trash's `user` or project-hash
subdirectory.

MCP files are read again; a member goes only when it still holds the value the scan
recorded; the re-read bytes go to the trash with `trash.CopyBytes` (0600) and the edit is
written with `fsx.WriteFileAtomic` to the resolved path. A unit that changed is skipped with a
notice; the first failed move stops the run, which reports what moved and still writes the keep
edits. Tidy writes no `lock.json`, manifest or trust record.
