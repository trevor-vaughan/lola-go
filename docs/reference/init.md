# `lola init`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola init [dir] [--format agent-plugins|lola]
```

Scaffold a module named after the directory: by default an Agent Plugins package
(`plugin.json`, one skill, one command under lola's `dev.getlola` namespace); `--format lola` writes `lola.yml`, one
skill and one command. It never overwrites a file and refuses a directory holding a module or a
symbolic link on the way to a file it would write. With no `dir` it scaffolds the current
directory, so it first lists the files it would write and asks `Create module <name> in <dir>?`
(default No). Naming the directory, `.` included, asks nothing. Its refusals (a bad name, an
unknown format, an existing module or file, a link) come before the question. Writing a module
is described in [Module layout](../guides/authoring-modules.md#module-layout).

| Flag | What it does |
|---|---|
| `--format <format>` | The module format: `agent-plugins` (the default) or `lola`. |
| `-y`, `--yes` | Do not ask for confirmation. |

Refusals:

- Off a terminal, with no `dir` and without `-y`: `lola init needs confirmation, and there is no
  terminal to ask on`, hinting the run with `-y` (`lola init -y`).
- `../my-module is already a module (it has plugin.json)`; a module in `lola` format names `lola.yml`.
- A directory name that is not a valid module name, which is refused before anything is written
  (the name is the directory's):
  - `agent-plugins`: up to 64 characters from `a-z`, `0-9`, `.` and `-`, starting and ending with a
    letter or digit, with no `--` or `..`.
  - `lola`: 1 to 64 characters from `a-z`, `0-9`, `.`, `_` and `-`, starting with a letter or
    digit, with no `..`.
- `lola init cannot scaffold format "nope"`.

With `--json`: `name`, `path`, `format` and `files` (the paths it writes, relative to `path`); the refusal off a terminal carries the same keys
beside `error` and `hints`.
