# Fetching sources

Maintainer notes on Fetching sources. It is split out of [architecture](../architecture.md).

`source.Parse` classifies an argument: a URL (`://`, but `file://`) or git's scp form (a Windows
drive path excepted) by its form alone, never looked up on disk, where a directory a checkout
names like it could stand in for it; anything else by what is on disk.
`source.Fetch` stages a module from a `Spec`. Both call
`source.Validate`, because the engine also builds `Spec`s from the ledger and the cache index
without parsing an argument, and a project's files may be someone else's.

`Validate` refuses
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
         module root → copy (ignore list, links as files) → Fetched{Dir, Name, Commit, Executables}
```

**Run staging.** Every engine operation that fetches (`InstallAll`, `InstallChoices`, `Fetch` for
`cache add` and Add a source…, `AddEntry`, `Update`, `Sync`, `Info`, `AddProfile`, `UpdateProfile`,
`AddMarket`, `UpdateMarkets` (one per marketplace, `updateMarket`) and `PythonCatalog`; `migrate`
goes through `InstallAll`) creates its own `run-*` under `$XDG_CACHE_HOME/lola/.staging/` (mode
`0700`, `Engine.runDir`) and removes it on every return path. `lola install` is the exception: its
flow opens one staged run (`Engine.StageInstall`, `engine.StagedRun`) before its first picker and
closes it when the command ends, on every path.

`InstallChoices` and `InstallAll` given it (`ChoicesRequest.Run`, `InstallSpec.Staged`) fetch into
it with `Store.Prepare` alone. They read what it holds as the cache will hold it: `Engine.origin`
lays `store.Staged.Index`, the index `Commit` would write, over the cache's own, so pins, folder
detection, listed refs and review hints see a staged version as cached.

`InstallAll` commits the versions its plan installs (`StagedRun.commit`) after the receipt and
before its first write; a version the run dropped is never committed. A call given no staged run
(lola migrate, lola info's plan) opens a publishing run, which commits each fetch at once. The
staged run holds only its directory's lock across the pickers, which no other lola waits on; the
sweep and `cache clean` skip it while it is held.

`InstallAll`, `InstallChoices`, `Update`, `Sync`, `Info`, `AddProfile`, `UpdateProfile`,
`AddMarket`, `UpdateMarkets` and `restoreRemotes` (which `InstallAll`, `Sync` and `Move` call to
restore remote profiles, and which skips the sweep on a dry run) first remove `run-*` directories
last modified more than a minute ago whose lock no live run holds (`Engine.sweepRuns`); the
sweep is best effort, and what it cannot remove is a warning notice, never a refusal.

Everything temporary goes inside: fetch scratch (`scratch-*`), fetched copies (`fetch-*`), the
reachability check's repository, version builds (`version-*`, assembled beside the staged tree by
`Store.Put`) and snapshots.

SIGINT and SIGTERM cancel the command's context (`cli.NotifyContext`, set on the root command in
`main`): the git, download or hook it waits on is stopped, `Engine.commit` refuses to start a write
on a cancelled context, the error unwinds through the deferred `done`, and `cli.Execute` returns 128
plus the signal's number, printing nothing but the error of a `profile --update` that wrote profiles
it did not apply (what was updated, and the `lola sync` that applies it); `main` then resets the
signal and sends it to lola itself (`reraise`, Unix), so the parent sees a death by the signal and a
calling shell script stops (Windows exits with the code).

An abort mapped from `context.Canceled` (`profile
--update`) is an abort only when no signal caused it (`cli.cancelled`). SIGINT lola was started
with ignored (a background job of a non-interactive shell) stays ignored (`signal.Ignored`); an
ignored SIGTERM does not, since Go installs its own handler for it at start. A process killed
outside lola's control (SIGKILL, a second Ctrl+C, which has the default effect again) leaves its
directory until the next sweep.

The sweep goes by the run directory's modification time, but a live run holds its lock however long
it is idle (at a confirmation prompt, or in a long fetch inside `scratch-*`), so only a dead run's
directory is swept. A dry run sweeps too, since the directories are lola's own temporaries and the
sweep precedes the dry-run check (`InstallAll`, `Sync`).

Each remote kind produces a local tree in a scratch directory in the run's directory, which is
removed on success and on error. The copy that every kind shares then runs over the
tree with an explicit **listing mode**: it never guesses for a fetched tree, so a `.git/`
inside an archive is never handed to git, and an archive extracted below the user's own
repository is never filtered by that repository's ignore rules.

- **git.**
  - Every git command starts with `-c core.hooksPath=/dev/null -c protocol.ext.allow=never -c
    core.fsmonitor=false -c core.autocrlf=false` and empty LFS filters; user input appears only
    after `--` or as `--branch=<ref>`.
  - Clones use `--no-checkout`; before any checkout lola writes `$GIT_DIR/info/attributes` (`*
    -text -eol -filter -ident -working-tree-encoding`, which outranks `.gitattributes`) and lists
    the tree with `ls-tree -r -z`:
    - a mode `120000` entry on the way to the module path (`--path`) is refused, and on Windows
      one inside it too (without `core.symlinks` git would check it out as a plain file); other
      links in it are resolved by the copy (`copyFiles`), and links elsewhere never reach it;
    - so is a `--path` that is no directory of the tree in its exact letter case (a
      case-insensitive checkout would find it);
    - `100755` entries are the version's executables.
  - Then `reset --hard` (or `checkout --detach` for a SHA) writes the bytes as committed.
  - The environment sets `GIT_TERMINAL_PROMPT=0` and `GIT_PROTOCOL_FROM_USER=0` and drops the
    variables that select or configure a repository (`GIT_DIR`, `GIT_CONFIG_PARAMETERS`, …); the
    folder fetcher's `git ls-files` uses the same prefix and environment.
  - A ref resolves through `git ls-remote` with full ref names, matched exactly. A full SHA that
    names no branch or tag is fetched with `init --object-format`, `fetch --depth 1` and
    `checkout --detach`; clones run with `-q`, so a failure shows git's errors, not its progress.
  - A remote command whose stderr says access was refused (in a `fatal:` line: `could not read
    Username`, `repository … not found`, `Authentication failed`; or ssh's `Permission denied (…)`;
    `remote:` sideband lines are never read) is a `source.AccessError` instead (`accessDenied`):
    - lola's message for the location after `--`, with `git ls-remote -- <location>` (plus `-c
      http.followRedirects=false` when the run had it, and `ssh-add` when the server takes keys) as
      hints,
    - but not for a github.com web or organisation page (`githubWebHint`), whose `AccessError.Web`
      names the repository, ref and path for the CLI to hint in the user's own command
      (`coachSource`, which also takes `store.SkillsDirError` and `source.TagObjectError`);
    - none for a location that is not plain or over `MaxName`;
    - callers keep them (`unhinted` passes it through).
  - One 5-minute context covers the fetch; `WaitDelay` (5 s) makes git stop on time even when a
    child keeps its output open, and a timeout is coached by the location's transport (ssh
    prompts, or the network for https).
  - git's stderr, which carries a remote's `remote:` lines, goes to a `headWriter` that keeps
    only its first 4 KiB and discards the rest, so a chatty or hostile remote cannot grow lola's
    memory.
- **Archives.**
  - Extraction writes through an `os.Root` opened on the scratch directory, so no entry resolves
    outside it even if a name check has a bug.
  - Only regular files and directories are created, files with `O_EXCL`; every entry is copied
    through a reader capped at its declared size plus one byte.
  - The entry, byte and ratio limits are constants that specs lower through an unexported
    parameter.
  - Before `archive/zip` parses a zip's central directory into memory, `checkZipDirectory` finds
    the end record (and the zip64 one) the way `archive/zip` does and refuses a declared count
    over the entry limit, or a declared size or actual span over 1 KiB per allowed entry:
    `archive/zip` reads records until one fails to parse, whatever the declared size, and checks
    the count only modulo 65536.
- **Downloads.** A clone of the default transport (proxies honored) with compression disabled, a
  2-minute timeout, at most 5 redirects, none from https to http and none to http off loopback, and
  a body capped at 256 MiB.
- **Untrusted text.**
  - git's stderr, the names of archive entries, candidates for the module root, the status text
    of HTTP responses and echoed URLs go through `untrusted.Text`.
  - `untrusted.Text` replaces control characters, invalid UTF-8, Unicode format characters (bidi
    overrides, zero-width characters, BOM) and line or paragraph separators with `?`, indents
    continuation lines, and caps the length.
  - A parsed URL is echoed as scheme, host and path only (`displayURL`); one that does not parse,
    as its scheme and `://…` only (`schemeOnly`); git's scp form, without its user, password,
    query or fragment (`DisplayURL`).
  - The redirect target of a failed download and refused `git://` URLs are not echoed at all.
  - OS errors lose their path.

Every fetch reports its **executables** from the source's own metadata: git's tree modes, an
archive's entry modes, a folder's file system. The digest takes execute bits from that list,
never from the file system lola copied to, so a platform without execute bits digests the same
content the same.

**Reachability.** A pinned git commit must be reachable from the entry's ref:
`source.CheckReachable` resolves the ref as a fetch does, fetches its history with
`--filter=tree:0` into a separate, freshly initialized scratch repository with
`GIT_NO_LAZY_FETCH=1`, and asks `git merge-base --is-ancestor`; a commit that is absent or not an
ancestor is refused. It is skipped when the ref is the commit itself.
