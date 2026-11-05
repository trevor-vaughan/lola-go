# `lola repair`

```
lola repair [-n] [-y]
```

Forget what `lock.json` records that lola could not have written: an artifact or `created` path
that leaves its target's root (`../`, an absolute path), and a target lola does not know. Every
other command that plans from it refuses such a `lock.json` and hints `lola repair` for its
scope ([Sync, pins and trust](../guides/sync-and-trust.md#sync-pins-and-trust)). It lists the entries,
asks (default No), then forgets them; a module left installed on no target lola knows is
forgotten with them. It deletes and moves no file.

| Flag | What it does |
|---|---|
| `-n`, `--dry-run` | List what it would forget and write nothing. |
| `-y`, `--yes` | Do not ask for confirmation. |

It prints `will forget 2 entries in <root>/.lola/lock.json; files are not touched:` and one line
per entry: `module kit: artifact "../outside.md" on target "claude-code"`, `module kit: target
"bogus"`, `created "../settings.json" on target "claude-code"`. After a run that forgot a path
on a target lola knows, or a whole module, it ends with `→ lola sync puts back any file a module
now lacks → lola sync --project`. With nothing to forget it prints `✓ nothing to repair in
<root>/.lola/lock.json` and exits 0.

A run that applies it prints `✓ forgot 2 entries in <root>/.lola/lock.json; no file was
touched:` (`1 entry` for one) and the same entry lines, then one `module <m>: no target lola
knows is left` line for each module forgotten whole, before the `→` lines.

Refusals: off a terminal without `-y`: `lola repair needs confirmation, and there is no terminal
to ask on`, hinting the run with `-y`. When another lola run changed `lock.json` while repair
waited for it: `<root>/.lola/lock.json changed since lola listed what to forget: <entry> is new`,
hinting `lola repair` again; nothing is written. A `lock.json` lola refuses for another reason
(malformed JSON, written by a newer lola, a source, profile or member lola never writes) keeps
that refusal and its hint.

JSON: `dry_run`, `file`, `forgotten` (each `entry`, `kind`, `module`, `target`, `path` and
`key`), `forgotten_modules` and `notices`. The refusal off a terminal carries the `-n --json`
keys beside `error` and `hints`.
