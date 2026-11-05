# `lola help`

```
lola help [command]
```

Show help: concise on a terminal; piped or redirected, the full command tree, with every alias. A command's own help (`lola install --help`)
starts with that command's description and usage, then the global flags and the commands below it.

| Flag | What it does |
|---|---|
| `-l`, `--long` | Print the full command tree. |
| `--short` | Print concise help. |

Refusals: `no help topic "nope"; see: lola help`.
