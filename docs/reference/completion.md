# `lola completion`

```
lola completion bash|zsh|fish|powershell
```

Print a completion script for your shell (`source <(lola completion bash)`). No shell, more than
one, or another shell is refused with the four shells named and a hint.

## Setting it up

| Shell | Once |
| --- | --- |
| bash | add `source <(lola completion bash)` to `~/.bashrc` (needs the `bash-completion` package) |
| zsh | `mkdir -p ~/.zfunc && lola completion zsh > ~/.zfunc/_lola`, then in `~/.zshrc`, **before** `compinit`: `fpath=(~/.zfunc $fpath)` and `autoload -Uz compinit && compinit` |
| fish | `lola completion fish > ~/.config/fish/completions/lola.fish` |
| PowerShell | add `lola completion powershell \| Out-String \| Invoke-Expression` to your profile |

zsh only loads a completion from a directory on `fpath` when `compinit` runs. `source <(lola
completion zsh)` before `compinit` fails with `command not found: compdef`; after `compinit`
it works for that shell. Open a new shell after the change.

It completes commands, their aliases and flags, and these values:

| Where | What completes |
| --- | --- |
| `lola install`, `lola check` arguments | modules in your cache and installed in this scope, and file names (a source can be a path); `@` completes to `@market/module` |
| `--skip`, `--only`, `--unskip` | the components (`kind/name`) of the modules named on the line, from your cache or, for a local folder, read in place (a plugin format needs a fetch first, so it offers none); in a comma-separated list, the last item |
| `--trust=` | the kinds the command takes (`all` alone); in a comma-separated list, the last item |
| `lola profile [<profile>] --edit` arguments | your profiles first, then the modules in your cache |
| `lola tidy` arguments, `--keep` | `skill/`, `command/`, `agent/`, `mcp/`, `rule/`: what can move comes from a scan that runs git, which completion never does |
| `lola tidy --unkeep` | the selectors on the scope's keep-list |
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
