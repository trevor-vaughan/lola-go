# `lola check`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola check [module-or-source...]
```

Report what each assistant would drop, adapt or skip from a module, without writing anything or
adding it to your cache. A source, or `@market/module`, is fetched as `lola cache add` would;
any other argument names a cached module. Defaults to the current directory
([Findings](../usage.md#findings)). It also names, per module, the components your picks leave out that the
ones you keep mention, with the `lola install … --with-mentions` that adds them
([Mentions](../guides/choosing-what-to-install.md#mentions)).

| Flag | What it does |
|---|---|
| `-t`, `--target <assistant>` | Only check these assistants. `<id>@<version>` (`opencode@1.0`) checks for that version in place of the one the scope declares, read as [`lola install`](install.md) reads it. |
| `--strict` | Exit with an error if any target would drop, change or skip part of a module. |

Refusals: an argument that is neither a source nor a cached module (`"nope" is neither a source nor a module in your cache;
pass a module folder or another source, e.g. lola check ./my-module; sources are …`, hinting
`cached modules: lola cache list`), an unknown target, a `-t` `<id>@<version>` whose version is not valid (with the run for each version), and `--strict` with a
finding.

JSON (`--json`, [Output contract](../usage.md#output-contract)): `modules` (each `name`, `targets`, `findings`, `skipped`, and `notices`: each `level`,
`text`, `class`, `[]` when none) and `findings`.
