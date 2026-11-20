# `lola help`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola help [command]
```

Show help for lola or a command. There are three forms, the same on a terminal and piped:

| Form  | Ask for it                                                      | It prints                                                                                                                                                                  |
|-------|-----------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Short | `lola <command> -h`, `lola help --short <command>`, bare `lola` | The command's short description, its usage and examples, and its own flags (no global flags), then `More: lola <command> --help`.                                          |
| Long  | `lola <command> --help`, `lola help <command>`                  | The long description, the full usage with the global flags, and a `Reference:` link to the command's page. The root's long help adds the environment variables lola reads. |
| All   | `lola help --all [command]`                                     | The global flags once, then the long help of the command and every command below it, every alias included.                                                                 |

When both `-h` and `--help` are given, `--help` wins. A flag's value is never read as a help flag
(`lola install -t --help -h` is short help), and nothing after `--` is a flag.

The `Reference:` link is derived from the module lola was built from: a release links to its own
tag's docs, any other build to `main`.

| Flag      | What it does                                                |
|-----------|-------------------------------------------------------------|
| `--all`   | Print the command and every command below it, as long help. |
| `--short` | Print short help, as `-h` does.                             |

The flags belong to `lola help` (`lola help --all install`), not to `lola install --help`; together they are refused: `--all and --short cannot be combined`.

Refusals: `no help topic "nope"; see: lola help`.
