# Trust kinds

A guide for lola users; back to the [README](../../README.md).

How trust kinds apply to an install. It is split out of [choosing-what-to-install](choosing-what-to-install.md).

Every question lola asks before it runs or installs something you did not write has a kind, and
`--trust` answers the kinds you name, on `sync`, `update`, `profile`, `install` and `migrate`:

| Kind            | Covers                                                                                                                   | Recorded in `$XDG_STATE_HOME/lola/` |
|-----------------|--------------------------------------------------------------------------------------------------------------------------|-------------------------------------|
| `entries`       | The changes to `project.yml`, its top-level targets, and the pins and folder content a sync would write                  | `trust.json`                        |
| `profiles`      | A project profile's changes, a project profile taking your profile's place, a remote profile `--add` or `--update` shows | `trust.json`                        |
| `scripts`       | A project's install scripts, per project, event and the digest of the script ([Install scripts](install-scripts.md))     | `consent.json`                      |
| `hooks`         | A module's install hooks, by the digest of their scripts                                                                 | `consent.json`                      |
| `runtime-hooks` | A module's Claude Code runtime hooks and agent MCP servers, per settings file                                            | `consent.json`                      |

Write the kinds after `=`, as a comma list or the flag repeated (`--trust=entries,hooks`). A
command's help shows `--trust[=entries,profiles]` when a bare `--trust` (no `=`) grants those two
kinds there, and `--trust=<kinds>` when a bare `--trust` grants none there.

`--trust hooks` is refused: it reads as bare `--trust` and a module named `hooks`. lola refuses an
argument right after a bare `--trust` that names kinds (in any case, or a list such as `hooks,scripts`; after `--` it
is a name), hinting your run with `--trust=` the kinds you wrote, never the ones a bare `--trust`
would add. `--trust=entries,profiles` written out is those two kinds, so `profile --add` refuses
it. Bare `--trust` is `entries,profiles`: lola runs no code for them, but accepting entries is also
your consent to the MCP servers they list, which your assistant launches. The trust diff prints
each server's command first. `all` is every kind the command takes.

## What lola has shown you

`scripts`, `hooks` and `runtime-hooks` are the consent kinds: they run code. `--trust` gives one
of them only for content lola has shown you in full, every byte of it (control characters
replaced), in any output it printed on this machine: a dry run (`-n`), a question, the install
review or a run's report, in human output, `--plain` or `--json`. Each install hook and project
script prints its body under its line (four spaces, then `| …`; `hook-line` and `script-line` rows under
`--plain`, `body` under `--json`) until it has been shown; after that the human output folds it
(`▸ pre.sh: 12 lines`). A runtime hook prints every field, its prompt uncut. lola remembers what it
showed by the sha256 of the content, not by module version or project, so the same hook shown for
another version or in another project counts as shown.

What does not count: a folded line (`▸`; `-v` prints it in full, which does), a path with its
sha256, a refusal (it names what it refuses but does not show it) and `lola info`'s first lines
of an install hook. An empty script is shown by its line. A script larger than 64 KiB, or not
UTF-8 text, or holding a NUL byte, is never shown: its line says so and names the file to
inspect, and only `--unseen` consents to it (`lola cannot show you this hook (too large);
inspect …, then consent to it unseen: … --unseen`). Human and `--plain` output print a body's
tabs as spaces to 8-column stops; `--json`'s `body` is the text as it is. `lola info` shows a runtime hook it renders in full, which counts; for install
hooks and scripts its `install it:` line follows `see its hooks and scripts in full: … -n`.

Content lola has not shown you is handled as if `--trust` did not cover it: on a terminal lola
asks; off one it declines the runtime hooks or post-install hook, or refuses a pre-install hook
or script, with the hints

```
hint: lola has not shown you this hook; see it with lola sync --trust=hooks -n, then run this again
hint: or add --unseen to consent without seeing it: lola sync --trust=hooks --unseen
```

A dry run prints every hook and project script the same run would want consent for, a
project's post-install script with nothing to install too, so its `trusted` list is what that
run grants. A refusal without `--trust` of a hook or script lola has not shown you hints the same
dry run first (`add -n to see it first, then consent to it: …`). The refusal labels each hash:
`pre-install: setup.sh (hook sha256 …); module digest sha256:…` (`script sha256` for a project
script), the module digest being what `--at` pins (a git module shows `module commit`). The report
of a run lists the runtime hooks and post-install hooks it declined in full, so the same
command works the next time. A hook changed since you saw it is new content, and is declined or refused again until it is shown.

`--unseen` widens the consent kinds `--trust` names to content lola has not shown you: use it in
CI and on a fresh machine, where no one reads the output first. Given without a consent kind in
`--trust` it is refused (`lola sync --unseen --trust=entries`), hinting the run without it.
`--force` never consents and never implies `--unseen`.

The record is `$XDG_STATE_HOME/lola/shown.json`, your own state: no project or cache holds it.
A dry run may add to it; it changes nothing in a project, and only remembers what it
showed you. If it is damaged, lola refuses to read it and says so; deleting it is safe, and costs
only showing each hook and script again before `--trust` consents to it.

## Kinds a command takes

A kind the command never asks is refused, naming the ones it takes: `install` and
`migrate` take `scripts`, `hooks` and `runtime-hooks` (bare `--trust` is refused there, beside
other kinds too), `profile --add` takes `profiles`, and `sync` and the other `profile` actions
take all five. An unknown kind is refused, hinting the closest one within two edits (a stray
comma, the value without it). `profile --update` without a scope flag refuses `--trust=entries`,
which would answer nothing there, hinting the run with `--project` and with `--scope both` (see
[Choosing a scope](choosing-what-to-install.md#choosing-a-scope)).

What the kinds cover is listed once, before anything is written, as a receipt on stderr:

```
trusting, as --trust=entries,hooks allows:
  → entries: targets (new), kit (new)
  → hooks: kit pre-install (new)
Proceed? [y/N]
```

On a terminal lola then asks once (`-y` answers; No installs and records nothing, and `install`, `sync` and
`update` leave the cache as it was); what the kinds leave uncovered is asked item by item first, as it is when you pass no flag.
`profile --add`, which takes no `-y`, asks it on a terminal too. After the [install review](choosing-what-to-install.md#the-install-flow) (also
`lola profile <name> --edit`'s), the Apply you answered is that confirmation for every item the
review listed (its runtime hooks): the receipt neither prints nor asks again, unless `--trust`
covers an item the review did not list, such as an install hook, when it asks as always.
`--force`, which prints the summary in the review's place, answers no Apply, so the receipt asks.

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

Runtime hooks you answered No to at a question are asked about again only after they change: they
stay declined, and the report folds them as `declined earlier` with the run that consents to them
(`lola sync --trust=runtime-hooks`), which works since the question showed them. Hooks declined
where nothing asked (off a terminal) are asked about on the next run on a terminal.

A grant by `--trust` is recorded as a yes at the prompt is, once the receipt is answered: a later
run on the same machine with the same digests asks nothing and needs no flag, and a changed
script, hook set or runtime-hook group asks again. A yes at a hook's own question is recorded at
the same point, with them (just before the first write when nothing is covered). A No, a refusal,
Ctrl+C or `-n` records nothing, neither the flag's grants nor those yes answers.

On a CI machine whose `$XDG_STATE_HOME` persists between jobs, one job's
`--trust=hooks` therefore covers later jobs that install the same hooks; give each job a fresh
`XDG_STATE_HOME` when you want consent per job.
