# `lola completion`

```
lola completion bash|zsh|fish|powershell
```

Print a completion script for your shell (`source <(lola completion bash)`). Another shell is
refused: `invalid argument "nope" for "lola completion"`.

It completes commands, their aliases and flags, and these values:

| Where | What completes |
| --- | --- |
| `lola install`, `lola check` arguments | modules in your cache and installed in this scope, and file names (a source can be a path); `@` completes to `@market/module` |
| `--skip`, `--only`, `--unskip` | the components (`kind/name`) of the modules named on the line, from your cache; in a comma-separated list, the last item |
| `--profile`, `--to` | `base` and your profiles |
| `--from`, `--bundle` | your profiles (`--bundle` as a comma-separated list) |
| `lola profile [<profile>] --move` | the modules of the profile named, else of the scope |
| `--at` | `<module>=` for each module installed in the scope, then the version installed (a commit, or `sha256:` digest) |
| `lola trash` arguments | trash runs, newest first, each with the command that made it; after a run, the files it holds |
| `-t` | the assistants found on this machine first, with where; the rest say `not found on this machine` |
| `--older-than`, `--unused-for` | example ages (`7d`, `30d`, `2w`; `never` for `--unused-for`) |
| `--path`, `-C`, `lola init` | directories |
| `--pre-install`, `--post-install`, `--add` | file names |
| `--ref`, `--ssh-host`, `--keep` and other free text | nothing |

Completion reads local files only: it writes nothing, never touches the network and offers
nothing when it cannot read what it needs.
