# `lola init`

```
lola init [dir] [--format agent-plugins|lola]
```

Scaffold a module named after the directory: by default an Agent Plugins package
(`plugin.json`, one skill, one `dev.getlola` command); `--format lola` writes `lola.yml`, one
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
- `../my-module is already a module (it has plugin.json)`.
- `lola init cannot scaffold format "nope"`.

JSON: `name`, `path`, `format` and `files`; the refusal off a terminal carries the same keys
beside `error` and `hints`.
