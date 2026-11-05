# Trust kinds

How trust kinds apply to an install. It is split out of [choosing-what-to-install](choosing-what-to-install.md).

Every question lola asks before it runs or installs something you did not write has a kind, and
`--trust` answers the kinds you name, on `sync`, `update`, `profile`, `install` and `migrate`:

| Kind | Covers | Recorded in |
|---|---|---|
| `entries` | The changes to `project.yml`, its top-level targets, and the pins and folder content a sync would write | `trust.json` |
| `profiles` | A project profile's changes, a project profile taking your profile's place, a remote profile `--add` or `--update` shows | `trust.json` |
| `scripts` | A project's install scripts, per project, event and the digest of the script ([Install scripts](#install-scripts)) | `consent.json` |
| `hooks` | A module's install hooks, by the digest of their scripts | `consent.json` |
| `runtime-hooks` | A module's Claude Code runtime hooks and agent MCP servers, per settings file | `consent.json` |

Write the kinds after `=`, as a comma list or the flag repeated (`--trust=entries,hooks`). A
command's help shows `--trust[=entries,profiles]` when a bare `--trust` (no `=`) grants those two
kinds there, and `--trust=<kinds>` when a bare `--trust` grants none there.

`--trust hooks` is bare `--trust` and a module named `hooks`: lola refuses an argument right after
a bare `--trust` that names kinds (in any case, or a list such as `hooks,scripts`; after `--` it
is a name), hinting your run with `--trust=` the kinds you wrote, never the ones a bare `--trust`
would add. `--trust=entries,profiles` written out is those two kinds, so `profile --add` refuses
it. Bare `--trust` is `entries,profiles`, the kinds that run no code; `all` is every kind the
command takes.

A kind the command never asks is refused, naming the ones it takes: `install` and
`migrate` take `scripts`, `hooks` and `runtime-hooks` (bare `--trust` is refused there, beside
other kinds too), `profile --add` takes `profiles`, and `sync` and the other `profile` actions
take all five. An unknown kind is refused, hinting the closest one within two edits (a stray
comma, the value without it). `profile --update` without a scope flag refuses `--trust=entries`,
which would answer nothing there, hinting the run with `--project` and with `--scope both`.

What the kinds cover is listed once, before anything is written, as a receipt on stderr:

```
trusting, as --trust=entries,hooks allows:
  → entries: targets (new), kit (new)
  → hooks: kit pre-install (new)
Proceed? [y/N]
```

On a terminal lola then asks once (`-y` answers; No installs and records nothing; `install` keeps
what it fetched in the cache, while `sync` and `update` leave the cache as it was); what the kinds leave uncovered is asked item by item first, as it is when you pass no flag.
`profile --add`, which takes no `-y`, asks it on a terminal too.

A command asks one receipt for
everything it does: `sync --scope both` runs each scope up to its receipt, shows one, and applies
the scopes only after its yes, and `profile --update` with `--trust` does the same for the
profiles and each scope's sync, writing no profile before it (a scope's sync refused before the
receipt still keeps the update, after a receipt of the profiles; a Cancel or a No to its removals
updates nothing; a profile another lola run replaced while you were asked is not updated, and
the profiles written before it are named, not applied).

Each scope records its grants just before it is applied, and one whose content
changed after the receipt is refused (`what --trust covers changed after you answered its receipt;
nothing more was written`). Off a terminal the run proceeds,
and under `--json` nothing is printed: the document's `trusted` key lists the same items, and so
does the error document of a run that fails after it recorded them (for several scopes or
destinations, what all of them recorded so far, `[]` for none).

A run whose
answers the kinds do not cover is refused off a terminal, and its hint is your run with
`--trust=` the kinds you gave and exactly the ones missing; a post-install hook or runtime hook
without consent is declined with a warning that names the run with its kind. `-y` and `--force`
never consent to any kind.

A grant by `--trust` is recorded as a yes at the prompt is, once the receipt is answered: a later
run on the same machine with the same digests asks nothing and needs no flag, and a changed
script, hook set or runtime-hook group asks again. A yes at a hook's own question is recorded at
the same point, with them (just before the first write when nothing is covered). A No, a refusal,
Ctrl+C or `-n` records nothing, neither the flag's grants nor those yes answers.

On a CI machine whose `$XDG_STATE_HOME` persists between jobs, one job's
`--trust=hooks` therefore covers later jobs that install the same hooks; give each job a fresh
`XDG_STATE_HOME` when you want consent per job.
