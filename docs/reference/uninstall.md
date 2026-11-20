# `lola uninstall`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola uninstall <module>...
lola uninstall --all
```

Aliases: `remove`, `rm`, `erase`, `delete`, `del`. Remove the module's entry from the layer it
comes from (the [profile](../guides/profiles.md#profiles) on, or the scope's base) and everything lola
wrote for it (on the `-t` targets only, if given: the entry keeps the others). The module stays in
your cache; `lola cache rm` removes it there. A module another layer still lists stays installed:
the notice says which layer and the command that removes it there too.

When a profile is on and
the module comes from the base, a terminal asks whether to remove it there; off one, `--profile
base` answers.

Asks before removing files; `-y` skips the question, and so does `--force`, which
also deletes files you edited since lola wrote them; `--force` never answers a consent question. A run that removes no files asks nothing, and
still writes. Scope as `lola install`'s: `--project`, `--user` and `--scope`
([Choosing a scope](../guides/choosing-what-to-install.md#choosing-a-scope)).

The directories those files leave empty go too, each printed as a step
(`→ delete claude-code .claude/commands/`; JSON `kind` `directory`, `path` without the slash), but
never the assistant's root (the project; at user scope your home, or OpenCode's config directory)
nor a directory directly in it, such as `.claude/`, `.gemini/` or `.opencode/`: lola looks for
those to tell which assistants are set up. Only an empty directory goes: one still holding
anything, or one the same run writes into, stays.

A symbolic link is never removed, nor the
directory it leads to, nor one above it; an emptied directory below a link goes when it lies
inside the assistant's root (`~/.claude` linked to `~/dotfiles/claude`: an emptied
`~/dotfiles/claude/commands/` goes), and none a link leads out of that root to.

Every run that removes files (`install`, `sync`, `update`, `profile`) cleans up empty directories and
skips symbolic links the same way.

A project uninstall that removes a module whole also clears the module's entry from the project's
section of `selections.yaml` (never the `global` section), so a later plain install installs
everything.

It prints `→ <module>: picks cleared: <selectors> (restore: lola install … --skip …)`:
the command, with your scope, `-t` and output flags (never `-y`: the install shows the files it deletes before it asks), saves them again
(`--skip` for `skip:`, `--unskip` for `include:`, `--only … --exact` for `only:`; see [Component flags](../guides/choosing-what-to-install.md#component-flags)). `-n` writes
nothing and says `picks would be cleared`.

A module that stays installed (another layer lists
it, or `-t` leaves it on another target) keeps its picks. JSON: `picks_cleared`, an array
(`[]` when none) of `module`, `section`, `skip`, `include`, `only` and `restore`.

Each entry removed prints a write line (`✓ removed mod1 from profile go  (<path>)`, see
[Where an install is written](install.md#where-an-install-is-written)). An edit to a profile that is off
says `saved to profile rust; it is not on at user scope → lola profile --user rust --switch`.

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Only uninstall from these assistants, of those it is installed on; the entry keeps the others, and writes `targets:` only when they differ from its file's. With `--profile` naming a layer the module does not come from (a lower layer, or a profile that is off), it narrows that entry's targets and removes no files. |
| `--all` | Remove every module `lola list` shows for the scope, the base's and the profile on's, in place of names. With `--profile`, the modules that profile's file lists (even one that is off). `--scope both` runs it at each scope. It asks once per scope, with every module in the plan, and answers which layer a module comes from, so a base module goes while a profile is on. Without `--profile` a module several layers list loses each entry, so nothing stays installed (plain `uninstall <module>` leaves it, naming the layer). Combines with `-t`, `--keep-edits` and `--force`. |
| `--profile <name>` | Remove the entry from this profile (`base` is the scope's base) instead of the layer the module comes from. |
| `-y`, `--yes` | Do not ask for confirmation. |
| `--force` | Also delete files edited since lola wrote them, without asking; what it replaces or deletes goes to a new trash run (`lola trash`). |
| `--keep-edits` | Leave files (and JSON members) edited since lola wrote them as they are, and stop managing them; everything else is removed as usual. Cannot be combined with `--force`. Still asks like a plain uninstall. |
| `-n`, `--dry-run` | Print the plan and write nothing. |

With `--keep-edits` each kept file prints `→ kept <path>: hand-edited; lola no longer manages it, so
installing the module again reports it as not written by lola` (`would keep` under `-n`), and a kept JSON
member names its file and key. The files leave `lock.json`, so installing the module again reports each as an
`unowned` conflict (`--force` then replaces it, into the trash). JSON: `kept`, an array (`[]` when none)
of those paths, and a `keep` step for each in `steps`.

Refusals:

- `lola uninstall: "my-module" is not installed in this project`, hinting `installed here: lola
  list --project`. When exactly one installed module matches the name ignoring case, a first hint
  says `did you mean my-module?` with the command, scope flags kept.
- The module comes from the base (`default (base)`, which `--profile base` names) while a profile is on, off a terminal: `my-module comes from
  default (base); lola uninstall needs to know whether to remove it there, and there is no
  terminal to ask on` (`change it there` under `-t`), hinting `lola uninstall my-module --user
  --profile base -y`. `-y` answers it when that is the one layer that lists the module; where
  several do, the refusal stays and its hint keeps your `-y` and adds `--profile <layer>`.
- A `--profile` that does not list it: `profile go does not list my-module`, hinting `see where it
  comes from: lola list --user`; an unknown one: `no profile nosuch for user scope`.
- `-t` naming an unknown assistant: `unknown target "nosuch"`, hinting the valid ones.
- `-t` naming no assistant the module is installed on: `my-module is not installed on gemini-cli;
  it is installed on claude-code`, hinting `uninstall it there: lola uninstall my-module -t
  claude-code` with your other flags kept.
  - The message adds where the entry lists the module when that differs from where it is installed (`it is listed for
    claude-code, opencode but installed only on claude-code`; for an entry never synced, `it is
    listed for claude-code but not installed there`, hinting `remove its entry instead: lola
    uninstall my-module`).
  - With `--profile` naming a layer the module does not come from, `-t` must name a target of
    that entry: `default (base) lists my-module for claude-code, not for opencode`.
- Off a terminal without `-y`, when files would be removed: `lola uninstall needs confirmation,
  and there is no terminal to ask on`, hinting the run with `-y` (`lola uninstall my-module -y`).
- A file edited since lola wrote it: `refusing to change anything: 1 conflict, which --force
  overrides`, each file on its own line, hinting two runs: `lola uninstall my-module --keep-edits`
  (leaves the edited files, your flags kept; `-y` only if you gave it, so off a terminal that run's own refusal hints it) and `lola uninstall my-module --force`
  (deletes them).
- `--all` with names: `--all removes every module, and my-module names some`, hinting the run without
  `--all` and the run without the names.
- `--all` with nothing installed prints `nothing to uninstall` and succeeds, also off a terminal without `-y`.
- `--keep-edits` with `--force`: `--keep-edits and --force cannot be combined; pick one`, hinting each
  run without the other.

JSON (`--json`, [Output contract](../usage.md#output-contract)): with `--all` over two scopes, one document, `scopes`, an array of the per-scope results below; otherwise `modules`, `steps`, `findings`, `notices`, `edits`, `scripts`, `kept` and `dry_run`; the refusal off a
terminal carries the same keys beside `error` and `hints`.
