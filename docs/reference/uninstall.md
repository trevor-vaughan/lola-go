# `lola uninstall`

```
lola uninstall <module>...
```

Aliases: `remove`, `rm`, `erase`, `delete`, `del`. Remove the module's entry from the layer it
comes from (the [profile](../guides/profiles.md#profiles) on, or the scope's base) and everything lola
wrote for it (on the `-t` targets only, if given: the entry keeps the others). The module stays in
your cache; `lola cache rm` removes it there. A module another layer still lists stays installed:
the notice says which layer and the command that removes it there too. When a profile is on and
the module comes from the base, a terminal asks whether to remove it there; off one, `--profile
base` answers. Asks before removing files; `-y` skips the question, and so does `--force`, which
also deletes files you edited since lola wrote them. A run that removes no files asks nothing, and
still writes. Scope as `lola install`'s.

The directories those files leave empty go too, each printed as a step
(`→ delete claude-code .claude/commands/`; JSON `kind` `directory`, `path` without the slash), but
never the assistant's root (the project; at user scope your home, or OpenCode's config directory)
nor a directory directly in it, such as `.claude/`, `.gemini/` or `.opencode/`: lola looks for
those to tell which assistants are set up. Only an empty directory goes: one still holding
anything, or one the same run writes into, stays. A symbolic link is never removed, nor the
directory it leads to, nor one above it; an emptied directory below a link goes when it lies
inside the assistant's root (`~/.claude` linked to `~/dotfiles/claude`: an emptied
`~/dotfiles/claude/commands/` goes), and none a link leads out of that root to. Every run that removes files (`sync`, `update`, `profile`) does the same.

A project uninstall that removes a module whole also clears the module's entry from the project's
section of `selections.yaml` (never the `global` section), so a later plain install installs
everything. It prints `→ <module>: picks cleared: <selectors> (restore: lola install … --skip …)`:
the command, with your scope, `-t` and output flags (never `-y`: the install shows the files it deletes before it asks), saves them again
(`--skip` for `skip:`, `--unskip` for `include:`, `--only … --exact` for `only:`). `-n` writes
nothing and says `picks would be cleared`. A module that stays installed (another layer lists
it, or `-t` leaves it on another target) keeps its picks. JSON: `picks_cleared`, an array
(`[]` when none) of `module`, `section`, `skip`, `include`, `only` and `restore`.

Each entry removed prints a write line (`✓ removed mod1 from profile go  (<path>)`, see
[Where an install is written](install.md#where-an-install-is-written)). An edit to a profile that is off
says `saved to profile rust; it is not on at user scope → lola profile --user rust --switch`.

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Only uninstall from these assistants, of those it is installed on; the entry keeps the others, and writes `targets:` only when they differ from its file's. With `--profile` naming a layer the module does not come from (a lower layer, or a profile that is off), it narrows that entry's targets and removes no files. |
| `--profile <name>` | Remove the entry from this profile (`base` is the scope's base) instead of the layer the module comes from. |
| `-y`, `--yes` | Do not ask for confirmation. |
| `--force` | Also delete files edited since lola wrote them, without asking. |
| `-n`, `--dry-run` | Print the plan and write nothing. |

Refusals:

- `lola uninstall: "my-module" is not installed in this project`, hinting `installed here: lola
  list --project`. When exactly one installed module matches the name ignoring case, a first hint
  says `did you mean my-module?` with the command, scope flags kept.
- The module comes from the base while a profile is on, off a terminal: `my-module comes from
  default (base); lola uninstall needs to know whether to remove it there, and there is no
  terminal to ask on` (`change it there` under `-t`), hinting `lola uninstall my-module --user
  --profile base -y`. `-y` answers it when that is the one layer that lists the module; where
  several do, the refusal stays and its hint keeps your `-y` and adds `--profile <layer>`.
- A `--profile` that does not list it: `profile go does not list my-module`, hinting `see where it
  comes from: lola list --user`; an unknown one: `no profile nosuch for user scope`.
- `-t` naming an unknown assistant: `unknown target "nosuch"`, hinting the valid ones.
- `-t` naming no assistant the module is installed on: `my-module is not installed on gemini-cli;
  it is installed on claude-code`, hinting `uninstall it there: lola uninstall my-module -t
  claude-code` with your other flags kept. The message names where its entry lists it but it is
  not installed (`it is listed for claude-code, opencode but installed only on claude-code`;
  for an entry never synced, `it is listed for claude-code but not installed there`, hinting
  `remove its entry instead: lola uninstall my-module`). With `--profile` naming a layer the
  module does not come from, `-t` must name a target of that entry: `default (base) lists
  my-module for claude-code, not for opencode`.
- Off a terminal without `-y`, when files would be removed: `lola uninstall needs confirmation,
  and there is no terminal to ask on`, hinting the run with `-y` (`lola uninstall my-module -y`).
- A file edited since lola wrote it: `refusing to change anything: 1 conflict, which --force
  overrides`, each file on its own line, hinting `lola uninstall my-module --force`.

JSON: `modules`, `steps`, `findings`, `notices`, `edits`, `scripts` and `dry_run`; the refusal off a
terminal carries the same keys beside `error` and `hints`.
