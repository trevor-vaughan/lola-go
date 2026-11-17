# `lola help`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola help [command]
```

Show help: concise on a terminal; piped or redirected, the full command tree, with every alias (each
command's other names). `--long` and `--short` pick one whichever the output. A command's own help (`lola install --help`)
starts with that command's description and usage, then the global flags and the commands below it.

| Flag | What it does |
|---|---|
| `-l`, `--long` | Print the full command tree. |
| `--short` | Print concise help. |

The flags belong to `lola help` (`lola help -l install`), not to `lola install --help`; together they are refused: `--long and --short cannot be combined`.

Refusals: `no help topic "nope"; see: lola help`.
