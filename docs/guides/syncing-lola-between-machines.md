# Syncing lola between machines

How to keep lola's state in step across machines. It is split out of [sync-and-trust](sync-and-trust.md).

Keep `~/.config/lola` in your dotfile manager (chezmoi, stow, yadm, a git repository). It holds
your profiles (`profiles/default.yml` and the others), `pins.yaml`, `config.yaml`,
`selections.yaml`, `markets.yaml` and `keep.yaml`. Picks for a project in a git clone are keyed by
its repository, so they apply wherever you clone it on the new machine; other projects' picks and
`keep.yaml` are keyed by path. On a new machine, restore it, then run
`lola sync --user`: lola installs the versions `pins.yaml` records, not what the refs serve
today, and fetches each remote profile you use at the version you accepted.

`pins.yaml` is written by lola: the version installed for each entry of your profiles at user
scope, and a record of each remote profile you have. It looks like this:

```yaml
# Written by lola: the versions installed from your profiles at user scope.
# Safe to sync between machines. Hand edits apply until lola next writes this file.
version: 1
modules:
  cc-skills-golang:
    - {source: 'https://github.com/samber/cc-skills-golang', ref: main, commit: 4f1c…, digest: 'sha256:…'}
remotes:
  go: {source: 'https://github.com/acme/lola-profiles', ref: main, path: profiles/go, commit: 77d2…, digest: 'sha256:…'}
```

- **When lola writes it.** Every user-scope run that installs, updates or removes entries of your
  profiles (`lola sync`, `lola update`, `lola install`, `lola uninstall` and the `lola profile`
  actions that change user scope) rewrites `modules:`, and only when its bytes change.
  - `lola profile --add`, `--update` and `--remove` keep `remotes:` current and leave `modules:` as
    it was.
  - A sync sets the pins of the modules it resolved or changed.
  - Any other run sets a pin
    only where it changed the version installed, or `pins.yaml` has none for the key; every other
    pin stays as it was read, so a newer pin synced from another machine survives
    `lola install other --user`, and `lola install kit -t gemini-cli --user`, which keeps kit's
    version.
  - A move between scopes keeps the version installed, and `pins.yaml`'s pin for the key
    with it, which the next `lola sync --user` applies.
- **What it never holds.** A folder, a local archive or a `file://` source is never pinned: the
  file holds no machine paths, consent or trust.
- **Header and links.** A comment at the top of the file is kept as you wrote it. A link
  (stow-style) is followed and stays a link, and the file keeps its mode (0600 when lola creates
  it). With chezmoi's copy model, run `chezmoi re-add` after a run that changed the file. Two
  machines updating at once conflict in your dotfile manager, not in lola.
- **Which pin wins.** At user scope an entry of your profiles takes its version from
  `pins.yaml`, else from `lock.json`; a pin applies only while the entry's `source`, `ref` and
  `path` equal its key, so an edited entry resolves afresh.
- **Moving the pins.** `lola update --user` moves them; the next sync of `~/.config/lola` carries
  them to your other machines, where `lola sync --user` applies them as changes.
- **Restoring a remote profile.** A run that needs a remote profile this machine lacks and
  `pins.yaml` records (`lola sync`, `lola install`, `lola profile --move`, `lola profile --switch`
  or `--on` of it, a remote member of the bundle on, or the profile a `--switch` or `--on` names)
  fetches it at the recorded commit before planning, checks the recorded digest, and writes it as
  `--add` does, with no question:
  `→ restored remote profile go from pins.yaml (https://github.com/acme/lola-profiles @ 77d2c1…)`.
  - Read-only commands do not fetch; `lola list` and `lola profile` show
    `profile remote:go (not on this machine yet → lola sync --user)`.
  - Under `-n` a run warns
    `remote:go is not on this machine yet; run this again without -n to restore it from pins.yaml`
    and fetches nothing.
- **When it refuses.** The source must pass the same host rules as `--add`, checked before any
  network contact.
  - If the commit is not on the ref any more, the run stops naming it
    (`go: commit <commit> is not on <ref> at <source>`); if the source no
    longer serves the recorded digest, it stops with `go: the source no longer serves the recorded
    version`. Both hint `review and take what it serves now: lola profile go --add <source> …`.
  - A network failure keeps its own message and hints running the same command again.
  - A file or link
    at the profile's place under `$XDG_DATA_HOME/lola/remote-profiles` is never written over: the run
    stops before it plans, naming it (`… is in the way of remote:go, which pins.yaml records; lola
    never writes over it`), and hints moving it aside; `lola list` shows it in place of the restore.
- **When it cannot record one.** `lola profile --add` or `--update` that wrote a profile but could
  not record it warns: a `pins.yaml` that does not load is named with its line, and a sync records
  the profiles once it is fixed by hand; otherwise a sync records them once the file can be
  written.
- **A `pins.yaml` from elsewhere** is checked like any input lola did not write on this machine:
  a name, source or pin lola would not have written refuses, naming the file and line, before
  anything is fetched.

`lock.json` and everything under `$XDG_STATE_HOME/lola` and `$XDG_DATA_HOME/lola` stay per
machine: the profile on is recorded there, so a new machine starts on the base.
